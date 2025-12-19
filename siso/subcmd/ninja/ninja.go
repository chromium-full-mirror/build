// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package ninja implements the subcommand `ninja` which parses a `build.ninja` file and builds the requested targets.
package ninja

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	log "github.com/golang/glog"
	"github.com/google/subcommands"
	"github.com/google/uuid"
	"github.com/klauspost/cpuid/v2"
	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"
	rspb "google.golang.org/genproto/googleapis/devtools/resultstore/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.chromium.org/build/siso/auth/cred"
	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/cachestore"
	"go.chromium.org/build/siso/build/ninjabuild"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/o11y/monitoring"
	"go.chromium.org/build/siso/o11y/resultstore"
	"go.chromium.org/build/siso/o11y/trace"
	"go.chromium.org/build/siso/reapi"
	"go.chromium.org/build/siso/reapi/digest"
	"go.chromium.org/build/siso/reapi/merkletree"
	"go.chromium.org/build/siso/signals"
	"go.chromium.org/build/siso/toolsupport/artfsutil"
	"go.chromium.org/build/siso/toolsupport/cogutil"
	"go.chromium.org/build/siso/toolsupport/ninjautil"
	"go.chromium.org/build/siso/toolsupport/soongutil"
	"go.chromium.org/build/siso/toolsupport/watchmanutil"
	"go.chromium.org/build/siso/ui"
	"go.chromium.org/build/siso/version"
)

// File name of siso metadata file.
// This file is read by ninjalog_uploader.py, in order to populate metadata.
const sisoMetadataFilename = "siso_metadata.json"

// SisoMetadata contains metadata that is populated directly by siso.
type SisoMetadata struct {
	// SisoVersion is the SemVer of siso.
	SisoVersion string `json:"siso_version"`
	// StartTime is the time that the ninja build started.
	StartTime time.Time `json:"start_time"`
	// BuildID is the Ninja build ID used for analytics and identification.
	BuildID string `json:"build_id"`
	// Targets of the build.
	Targets []string `json:"targets,omitempty"`
	// MetricsLabels are arbitrary labels for the build.
	MetricsLabels map[string]string `json:"metrics_labels,omitempty"`
}

const ninjaUsage = `build the requested targets as ninja.

 $ siso ninja [-C <dir>] [options] [targets...]

`

// Cmd returns the Command for the `ninja` subcommand provided by this package.
func Cmd(authOpts cred.Options, version string) *Command {
	return &Command{
		authOpts: authOpts,
		version:  version,
	}
}

// Command implements ninja subcommand.
type Command struct {
	authOpts cred.Options
	version  string
	started  time.Time

	Flags *flag.FlagSet

	NinjaFlags

	sisoInfoLog string // abs or relative to logDir
	startDir    string

	resultstoreUploader *resultstore.Uploader
}

func (*Command) Name() string {
	return "ninja"
}

func (*Command) Synopsis() string {
	return "build the requests targets as ninja"
}

func (*Command) Usage() string {
	return ninjaUsage
}

func (c *Command) Execute(ctx context.Context, flagSet *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	c.Flags = flagSet
	c.started = time.Now()
	err := parseFlagsFully(flagSet)
	if err != nil {
		ui.Default.Errorf("%v\n", err)
		return subcommands.ExitUsageError
	}
	if c.stateDir == "" {
		c.stateDir = filepath.Dir(c.fname)
	}
	if c.logDir == "" {
		c.logDir = filepath.Dir(c.fname)
	}
	if c.quiet {
		ui.Default = quietUI{}
	} else if c.frontendFile != "" {
		f := os.Stdout
		if c.frontendFile != "-" {
			f, err = os.OpenFile(c.frontendFile, os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				ui.Default.Errorf("failed to open frontend file: %v\n", err)
				return 1
			}
			defer func() {
				err = f.Close()
				if err != nil {
					ui.Default.Errorf("failed to close frontend file: %v\n", err)
				}
			}()
			// defer digest calculation for fast nop build on android
			c.fsopt.DeferDigest = true
		}
		frontend := soongutil.NewFrontend(ctx, f)
		ui.Default = frontend
		defer frontend.Close()
	}

	stats, err := c.run(ctx)
	d := time.Since(c.started)
	sps := float64(stats.Done-stats.Skipped) / d.Seconds()
	dur := ui.FormatDuration(d)
	if err != nil {
		var errFlag flagError
		var errBuild buildError
		switch {
		case errors.Is(err, errNothingToDo):
			msgPrefix := "Everything is up-to-date"
			if ui.IsTerminal() {
				msgPrefix = ui.SGR(ui.Green, msgPrefix)
			}
			ui.Default.Warningf("%s Nothing to do.\n", msgPrefix)
			return 0

		case errors.As(err, &errFlag):
			ui.Default.Errorf("%v\n", err)

		case errors.As(err, &errBuild):
			var errTarget build.TargetError
			if errors.As(errBuild.err, &errTarget) {
				msgPrefix := "Schedule Failure"
				if ui.IsTerminal() {
					dur = ui.SGR(ui.Bold, dur)
					msgPrefix = ui.SGR(ui.BackgroundRed, msgPrefix)
				}
				ui.Default.Errorf("\n%6s %s: %v\n", dur, msgPrefix, errTarget)
				if len(errTarget.Suggests) > 0 {
					var sb strings.Builder
					fmt.Fprintf(&sb, "Did you mean:")
					for _, s := range errTarget.Suggests {
						fmt.Fprintf(&sb, " %q", s)
					}
					fmt.Fprintln(&sb, " ?")
					ui.Default.Warningf("%s\n", sb.String())
				}
				return 1
			}
			var errMissingSource build.MissingSourceError
			if errors.As(errBuild.err, &errMissingSource) {
				msgPrefix := "Schedule Failure"
				if ui.IsTerminal() {
					dur = ui.SGR(ui.Bold, dur)
					msgPrefix = ui.SGR(ui.BackgroundRed, msgPrefix)
				}
				ui.Default.Errorf("\n%6s %s: %v\n", dur, msgPrefix, errMissingSource)
				return 1
			}
			msgPrefix := "Build Failure"
			if ui.IsTerminal() {
				dur = ui.SGR(ui.Bold, dur)
				msgPrefix = ui.SGR(ui.BackgroundRed, msgPrefix)
			}
			ui.Default.Errorf("\n%6s %s: %d done, %d failed, %d remaining - %.02f/s\n %v\n", dur, msgPrefix, stats.Done-stats.Skipped, stats.Fail, stats.Total-stats.Done, sps, errBuild.err)
			suggest := fmt.Sprintf("see %s for full command line and output", c.logFilename(c.outputLogFile, c.startDir))
			if c.sisoInfoLog != "" {
				suggest += fmt.Sprintf("\n or %s", c.logFilename(c.sisoInfoLog, c.startDir))
			}
			failedCommandsFile := c.logFilename(c.failedCommandsFile, "")
			if failedCommandsFile != "" {
				_, err := os.Stat(failedCommandsFile)
				if err == nil {
					suggest += fmt.Sprintf("\nuse %s to re-run failed commands", c.logFilename(c.failedCommandsFile, c.startDir))
				}
			}
			if ui.IsTerminal() {
				suggest = ui.SGR(ui.Bold, suggest)
			}
			ui.Default.Warningf("%s\n", suggest)
		default:
			msgPrefix := "Error"
			if ui.IsTerminal() {
				msgPrefix = ui.SGR(ui.BackgroundRed, msgPrefix)
			}
			if status.Code(err) == codes.Unavailable {
				ui.Default.Errorf("\n%6s %s: could not connect to backend. If you want to build offline, pass `-o` or `--offline`\n %v\n", ui.FormatDuration(time.Since(c.started)), msgPrefix, err)
			} else {
				ui.Default.Errorf("\n%6s %s: %v\n", ui.FormatDuration(time.Since(c.started)), msgPrefix, err)
			}
		}
		return subcommands.ExitFailure
	}
	msgPrefix := "Build Succeeded"
	if ui.IsTerminal() {
		dur = ui.SGR(ui.Bold, dur)
		msgPrefix = ui.SGR(ui.Green, msgPrefix)
	}
	ui.Default.Warningf("%6s %s: %d steps - %.02f/s\n", dur, msgPrefix, stats.Done-stats.Skipped, sps)
	return subcommands.ExitSuccess
}

// parse flags without stopping at non flags.
func parseFlagsFully(flagSet *flag.FlagSet) error {
	var targets []string
	for {
		args := flagSet.Args()
		if len(args) == 0 {
			break
		}
		argsRemaining := len(args)
		for i, arg := range args {
			if !strings.HasPrefix(arg, "-") {
				targets = append(targets, arg)
				argsRemaining--
				continue
			}
			err := flagSet.Parse(args[i:])
			if err != nil {
				return err
			}
			break
		}
		if argsRemaining == 0 {
			break
		}
	}
	// targets are non-flags. set it to Args.
	return flagSet.Parse(targets)
}

const (
	// relative to -state_dir
	failedTargetsFile = ".siso_failed_targets"
)

func (c *Command) run(ctx context.Context) (stats build.Stats, err error) {
	// Cleanup functions to run after serial cleanups in parallel.
	// This mostly exists for logger and metrics functions cleanup.
	// Each of these functions take about 1 second on no-op builds to finish,
	// so to speed things up these 2 cleanups run in parallel, reducing 1 second or above from the build times.
	var pCleanups []func()

	defer func() {
		var wg sync.WaitGroup
		spin := ui.Default.NewSpinner()
		spin.Start("shutdown cloud logging/monitoring")
		for _, cleanup := range pCleanups {
			wg.Go(cleanup)
		}
		wg.Wait()
		spin.Stop(nil)
	}()

	ctx, cancel := context.WithCancelCause(ctx)
	defer signals.HandleInterrupt(ctx, func() {
		cancel(errInterrupted{})
	})()
	err = c.debugMode.check()
	if err != nil {
		return stats, flagError{err: err}
	}
	switch c.subtool {
	case "":
	case "list":
		return stats, flagError{
			err: errors.New(`ninja subtools:
  commands   Use "siso query commands" instead
  deps       Use "siso query deps" instead
  inputs     Use "siso query inputs" instead
  targets    Use "siso query targets" instead
  cleandead  clean built files that are no longer produced by the manifest`),
		}
	case "commands":
		return stats, flagError{
			err: errors.New("use `siso query commands` instead"),
		}
	case "deps":
		return stats, flagError{
			err: errors.New("use `siso query deps` instead"),
		}
	case "inputs":
		return stats, flagError{
			err: errors.New("use `siso query inputs` instead"),
		}
	case "targets":
		return stats, flagError{
			err: errors.New("use `siso query targets` instead"),
		}

	case "cleandead":
		c.cleandead = true
	default:
		return stats, flagError{err: fmt.Errorf("unknown tool %q", c.subtool)}
	}

	if c.ninjaJobs >= 0 {
		ui.Default.Warningf("-j is not supported. use -remote_jobs and -local_jobs instead\n")
	}
	if c.ninjaLoadLimit >= 0 {
		ui.Default.Warningf("-l is not supported.\n")
	}
	if c.failuresAllowed <= 0 {
		c.failuresAllowed = math.MaxInt
	}
	if c.failuresAllowed > 1 {
		c.fastLastFailure = false
	}

	if c.adjustWarn != "" {
		ui.Default.Warningf("-w is specified. but not supported. b/288807840\n")
	}

	if c.offline {
		ui.Default.Warningf(ui.SGR(ui.Red, "offline mode\n"))
		clog.Warningf(ctx, "offline mode")
		c.reopt = new(reapi.Option)
		c.reopt.Insecure = true
		c.projectID = ""
		c.enableCollector = false
		c.enableCloudLogging = false
		c.enableResultstore = false
		c.enableCloudProfiler = false
		c.enableCloudTrace = false
		c.enableCloudMonitoring = false
		c.reproxyAddr = ""
	}

	execRoot, err := c.initWorkdirs(ctx)
	if err != nil {
		return stats, err
	}

	if c.stateDir != "." && c.fsopt.StateFile != "" {
		c.fsopt.StateFile = filepath.Join(c.stateDir, c.fsopt.StateFile)
	}
	lockFilename := filepath.Join(c.stateDir, ".siso_lock")
	if !c.dryRun {
		lock, err := newLockFile(ctx, lockFilename)
		switch {
		case errors.Is(err, errors.ErrUnsupported):
			clog.Warningf(ctx, "lockfile is not supported")
		case err != nil:
			return stats, err
		default:
			var owner string
			spin := ui.Default.NewSpinner()
			for {
				err = lock.Lock()
				alreadyLocked := &errAlreadyLocked{}
				if errors.As(err, &alreadyLocked) {
					if owner != alreadyLocked.owner {
						if owner != "" {
							spin.Done("lock holder %s completed", owner)
						}
						owner = alreadyLocked.owner
						spin.Start("waiting for lock holder %s..", owner)
					}
					select {
					case <-ctx.Done():
						return stats, context.Cause(ctx)
					case <-time.After(500 * time.Millisecond):
						continue
					}
				} else if err != nil {
					spin.Stop(err)
					return stats, err
				}
				if owner != "" {
					spin.Done("lock holder %s completed", owner)
				}
				break
			}
			defer func() {
				err := lock.Unlock()
				if err != nil {
					ui.Default.Errorf("failed to unlock %s: %v\n", lockFilename, err)
				}
				err = lock.Close()
				if err != nil {
					ui.Default.Errorf("failed to close %s: %v\n", lockFilename, err)
				}
			}()
		}
	}

	err = c.initLogDir(ctx)
	if err != nil {
		return stats, err
	}
	clog.Infof(ctx, "siso log dir=%s", c.logDir)

	resetCrashOutput, err := c.setupCrashOutput(ctx)
	if err != nil {
		return stats, err
	}
	defer resetCrashOutput()

	buildPath := build.NewPath(execRoot, c.dir)

	// compute default limits based on fstype of work dir (e.g. artfs),
	// not of exec root.
	limits := build.DefaultLimits(ctx)
	if c.localJobs > 0 {
		limits.Local = c.localJobs
	}
	if c.remoteJobs > 0 {
		limits.Remote = c.remoteJobs
		limits.REWrap = c.remoteJobs
	}
	if !c.fastLocal {
		limits.FastLocal = 0
		limits.StartLocal = 0
	}

	if err = uuid.Validate(c.buildID); err != nil {
		return stats, flagError{err: fmt.Errorf("%q is an invalid build ID. -build_id must be a UUID", c.buildID)}
	}
	if len(c.jobID) > 1024 {
		return stats, flagError{err: fmt.Errorf("-job_id length must be less than 1024")}
	}

	projectID := c.reopt.UpdateProjectID(c.projectID)

	var credential cred.Cred
	if !c.offline && (c.reopt.NeedCred() || c.enableCloudLogging || c.enableResultstore || c.enableCloudProfiler || c.enableCloudTrace || c.enableCloudMonitoring) {
		// TODO: can be async until cred is needed?
		spin := ui.Default.NewSpinner()
		spin.Start("init credentials by %q", c.authOpts.Type)
		credential, err = cred.New(ctx, c.reopt.ServiceURI(), c.authOpts)
		if err != nil {
			spin.Stop(errors.New(""))
			return stats, err
		}
		spin.Stop(nil)
	}
	if c.enableCloudLogging {
		spin := ui.Default.NewSpinner()
		spin.Start("init cloud logging")
		logCtx, loggerURL, done, err := c.initCloudLogging(ctx, projectID, execRoot, credential)
		spin.Stop(err)
		if err != nil {
			// b/335295396 Compile step hitting write requests quota
			// rather than build fails, fallback to glog.
			ui.Default.Warningf("fallback to glog\n")
			c.enableCloudLogging = false
		} else {
			// use stderr for confirm no-op step. b/288534744
			ui.Default.Warningf("%s\n", loggerURL)
			pCleanups = append(pCleanups, done)
			ctx = logCtx
		}
	}
	// logging is ready.
	var properties resultstore.Properties
	properties.Add("dir", c.dir)
	info := cpuinfo()
	clog.Infof(ctx, "%s", info)
	properties.Add("cpu", info)
	info = gcinfo()
	clog.Infof(ctx, "%s", info)
	properties.Add("memgc", info)

	clog.Infof(ctx, "siso version %s", c.version)

	ver, err := version.Current()
	if err != nil {
		clog.Warningf(ctx, "version err: %v", err)
	} else if ver.IsProdCIPD() {
		clog.Infof(ctx, "CIPD package name: %s", ver.CIPD.PackageName)
		clog.Infof(ctx, "CIPD instance ID: %s", ver.CIPD.InstanceID)
		properties.Add("cipd_package_name", ver.CIPD.PackageName)
		properties.Add("cipd_instance_id", ver.CIPD.InstanceID)
	} else if ver.Build != nil {
		clog.Infof(ctx, "Go version: %s", ver.Build.GoVersion)
		properties.Add("go_version", ver.Build.GoVersion)
		clog.Infof(ctx, "module %s %s %s", ver.Build.Main.Path, ver.Build.Main.Version, ver.Build.Main.Sum)
		properties.Add("go_module_path", ver.Build.Main.Path)
		properties.Add("go_module_version", ver.Build.Main.Version)
		properties.Add("go_module_sum", ver.Build.Main.Sum)
		bs := ver.BuildSettings()
		for _, k := range slices.Sorted(maps.Keys(bs)) {
			v := bs[k]
			clog.Infof(ctx, "%s=%s", k, v)
			properties.Add(k, v)
		}
	}
	c.checkResourceLimits(ctx, limits)

	properties.Add("job_id", c.jobID)
	clog.Infof(ctx, "job id: %q", c.jobID)
	clog.Infof(ctx, "build id: %q", c.buildID)
	clog.Infof(ctx, "project id: %q", projectID)
	clog.Infof(ctx, "commandline %q", os.Args)
	clog.Infof(ctx, "is_terminal=%t fast_nop=%t fast_local=%t fast_last_failure=%t fast_exit=%t", ui.IsTerminal(), c.fastNop, c.fastLocal, c.fastLastFailure, c.fastExit)

	spin := ui.Default.NewSpinner()

	if c.enableResultstore {
		c.resultstoreUploader, err = resultstore.New(ctx, resultstore.Options{
			InvocationID:  c.buildID,
			Invocation:    c.invocation(ctx, c.buildID, projectID, execRoot, properties),
			ClientOptions: credential.ClientOptions(),
		})
		if err != nil {
			return stats, err
		}
		ui.Default.Warningf("https://btx.cloud.google.com/invocations/%s\n", c.buildID)
		defer func() {
			spin.Start("finishing upload to resultstore")
			exitCode := 0
			if err != nil {
				exitCode = 1
			}
			cerr := c.resultstoreUploader.Close(ctx, exitCode)
			if cerr != nil {
				clog.Warningf(ctx, "failed to close resultstore: %v", cerr)
			}
			spin.Stop(cerr)
		}()
	}
	if c.enableCloudProfiler {
		c.initCloudProfiler(ctx, projectID, credential)
	}
	metricsLabels := make(map[string]string)
	for l := range strings.SplitSeq(c.metricsLabels, ",") {
		kv := strings.Split(l, "=")
		if len(kv) != 2 {
			clog.Warningf(ctx, "metrics label must be in the form key=value. got %q", l)
			continue
		}
		metricsLabels[kv[0]] = kv[1]
	}
	if c.enableCloudMonitoring && c.reproxyAddr == "" {
		metricsProject := projectID
		if c.metricsProject != "" {
			metricsProject = c.metricsProject
		}
		e, err := c.initCloudMonitoring(ctx, credential, metricsProject, projectID, metricsLabels)
		if err != nil {
			return stats, err
		}
		// Export all the metrics before shutting down as we still need the cloud logger to be present.
		defer func() {
			// Report build metrics.
			var cacheHitRatio float64
			if stats.CacheHit+stats.Remote > 0 {
				cacheHitRatio = float64(stats.CacheHit) / float64(stats.CacheHit+stats.Remote)
			}
			isErr := err != nil && !errors.Is(err, errNothingToDo)
			monitoring.ExportBuildMetrics(ctx, time.Since(c.started), cacheHitRatio, isErr)
		}()
		pCleanups = append(pCleanups, func() {
			// Cloud logger is getting shut down in parallel, report locally.
			otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
				log.Warningf("failed to export to OpenTelemetry: %v", err)
			}))
			shutdownStart := time.Now()
			cerr := e.Shutdown(ctx)
			shutdownDuration := time.Since(shutdownStart)
			log.Infof("cloud monitoring shutdown took: %s", shutdownDuration)
			if cerr != nil {
				log.Warningf("failed to close Cloud monitoring exporter: %v", cerr)
			}
		})
	}
	var traceExporter *trace.Exporter
	if c.enableCloudTrace {
		traceExporter = c.initCloudTrace(ctx, projectID, credential)
		defer func() {
			closeStart := time.Now()
			traceExporter.Close(ctx)
			closeDuration := time.Since(closeStart)
			clog.Infof(ctx, "cloud trace shutdown took: %s", closeDuration)
		}()
	}
	// upload build pprof

	targets := c.Flags.Args()
	config, err := c.initConfig(ctx, execRoot, targets)
	if err != nil {
		return stats, err
	}

	failedTargetsFilename := filepath.Join(c.stateDir, failedTargetsFile)

	var eg errgroup.Group
	var localDepsLog *ninjautil.DepsLog
	eg.Go(func() error {
		depsLog, err := initDepsLog(ctx, c.stateDir, c.depsLogFile)
		if err != nil {
			return err
		}
		localDepsLog = depsLog
		return nil
	})

	if err := c.reopt.CheckValid(); err == nil {
		ui.Default.Infof(fmt.Sprintf("use %s\n", c.reopt))
	} else {
		if c.strictRemote {
			return stats, flagError{err: fmt.Errorf("no reapi specified, but remote is requested as --strict_remote: %w", err)}
		}
		if c.remoteJobs > 0 && c.reproxyAddr == "" {
			return stats, flagError{err: fmt.Errorf("no reapi specified, but remote is requested as --remote_jobs=%d: %w", c.remoteJobs, err)}
		}
	}
	ds, err := c.initDataSource(ctx, credential)
	if err != nil {
		return stats, err
	}
	defer func() {
		err := ds.Close(ctx)
		if err != nil {
			clog.Errorf(ctx, "close datasource: %v", err)
		}
	}()
	c.fsopt.DataSource = ds
	c.fsopt.OutputLocal, err = initOutputLocal(c.outputLocalStrategy)
	if err != nil {
		return stats, err
	}
	if c.logDir == "." || c.logDir == filepath.Join(execRoot, c.dir) {
		cwd := filepath.Join(execRoot, c.dir)
		// ignore siso files not to be captured by ReadDir
		// (i.g. scandeps for -I.)
		clog.Infof(ctx, "ignore siso files in %s", cwd)
		c.fsopt.Ignore = func(ctx context.Context, fname string) bool {
			dir, base := filepath.Split(fname)
			// allow siso prefix in other dir.
			// e.g. siso.gni exists in build/config/siso.
			if filepath.Clean(dir) != cwd {
				return false
			}
			if strings.HasPrefix(base, ".siso_") {
				return true
			}
			if strings.HasPrefix(base, "siso.") {
				return true
			}
			if strings.HasPrefix(base, "siso_") {
				return true
			}
			if base == ".ninja_log" {
				return true
			}
			return false
		}
	} else {
		// expect logDir is out of exec root.
		clog.Infof(ctx, "ignore .ninja_log")
		ninjaLogFname := filepath.Join(execRoot, c.dir, ".ninja_log")
		c.fsopt.Ignore = func(ctx context.Context, fname string) bool {
			return fname == ninjaLogFname
		}
	}
	cogfs, err := cogutil.New(ctx, execRoot)
	if err != nil && !errors.Is(err, errors.ErrUnsupported) {
		clog.Warningf(ctx, "unable to use cog? %v", err)
	}
	if cogfs != nil {
		ui.Default.PrintLines(ui.SGR(ui.Yellow, fmt.Sprintf("build in cog: %s\n", cogfs.Info())))
		c.fsopt.CogFS = cogfs
	}
	if c.artfsDir != "" && c.artfsEndpoint != "" {
		artfs, err := artfsutil.New(ctx, c.artfsDir, c.artfsEndpoint)
		if err != nil {
			return stats, err
		}
		ui.Default.PrintLines(ui.SGR(ui.Yellow, "build on artfs\n"))
		c.fsopt.ArtFS = artfs
	}

	if fsmonitor := os.Getenv("SISO_FSMONITOR"); fsmonitor != "" {
		var fsmonitorPath string
		if !filepath.IsAbs(fsmonitor) {
			fsmonitorPath, err = exec.LookPath(fsmonitor)
			if err != nil {
				clog.Warningf(ctx, "failed to find fsmonitor %q: %v", fsmonitor, err)
				ui.Default.Warningf(ui.SGR(ui.BackgroundRed, fmt.Sprintf("SISO_FSMONITOR=%q: failed %v\n", fsmonitor, err)))
			}
		} else {
			fsmonitorPath = fsmonitor
		}
		if fsmonitorPath != "" {
			fsm := strings.TrimSuffix(filepath.Base(fsmonitor), filepath.Ext(fsmonitor))
			switch fsm {
			case "watchman":
				fsm, err := watchmanutil.New(ctx, fsmonitorPath, execRoot)
				if err != nil {
					clog.Warningf(ctx, "failed to initialize watchman: %v", err)
					ui.Default.Errorf(ui.SGR(ui.BackgroundRed, fmt.Sprintf("SISO_FSMONITOR=watchman: failed %v\n", err)))
				} else {
					ui.Default.Infof(ui.SGR(ui.Yellow, fmt.Sprintf("use watchman as fsmonitor: %s\n", fsmonitorPath)))
					c.fsopt.FSMonitor = fsm
				}
			default:
				ui.Default.Errorf(ui.SGR(ui.BackgroundRed, fmt.Sprintf("unknown SISO_FSMONITOR=%q (%q)\n", fsmonitor, fsm)))
			}
		}
	}

	spin.Start("loading fs state")

	hashFS, err := hashfs.New(ctx, *c.fsopt)
	spin.Stop(err)
	if err != nil {
		return stats, err
	}
	defer func() {
		if c.dryRun {
			return
		}
		if c.subtool != "" {
			// don't modify .siso_failed_targets by subtool
			return
		}
		if c.prepare {
			// don't modify .siso_failed_targets for prepare (ide query).
			return
		}
		if err != nil {
			// Even when batch mode, it records failed targets.
			// It will be read by Chromium recipe.
			var errBuild buildError
			if !errors.As(err, &errBuild) {
				return
			}
			var stepError build.StepError
			if !errors.As(errBuild.err, &stepError) {
				rerr := os.Remove(c.logFilename(c.failedCommandsFile, ""))
				if rerr != nil {
					clog.Warningf(ctx, "failed to remove failed command file: %v", rerr)
				}
				return
			}
			// store failed targets only when build steps failed.
			// i.e., don't store with error like context canceled, etc.
			clog.Infof(ctx, "record failed targets: %q", stepError.Target)
			serr := saveTargets(failedTargetsFilename, targets, []string{stepError.Target})
			if serr != nil {
				clog.Warningf(ctx, "failed to save failed targets: %v", serr)
				return
			}
		} else {
			rerr := os.Remove(c.logFilename(c.failedCommandsFile, ""))
			if rerr != nil {
				clog.Warningf(ctx, "failed to remove failed command file: %v", rerr)
			}
		}
	}()
	defer func() {
		hashFS.SetBuildTargets(ctx, targets, !c.dryRun && c.subtool == "" && !c.prepare && err == nil)
		err := hashFS.Close(ctx)
		if err != nil {
			clog.Errorf(ctx, "close hashfs: %v", err)
		}
	}()
	hashFSErr := hashFS.LoadErr()
	if hashFSErr != nil {
		ui.Default.Errorf(ui.SGR(ui.BackgroundRed, fmt.Sprintf("unable to do incremental build as fs state is corrupted: %v\n", hashFSErr)))
	}

	_, err = os.Stat(failedTargetsFilename)
	lastFailed := err == nil
	isClean := hashFS.IsClean(targets)
	clog.Infof(ctx, "hashfs loaderr: %v clean: %t (%q) last failed: %t", hashFSErr, isClean, targets, lastFailed)
	// In prepare mode for ide_query, it won't record .siso_failed_targets
	// and won't match with .siso_fs_state.
	// in this case, don't shortcut noop build, but better to check
	// build graph again.
	if !c.clobber && c.fastNop && !c.dryRun && !c.debugMode.Explain && c.subtool != "cleandead" && !c.prepare && hashFSErr == nil && isClean && !lastFailed {
		// TODO: better to check digest of .siso_fs_state?
		return stats, errNothingToDo
	}

	if c.resultstoreUploader != nil {
		defer func() {
			var ents []merkletree.Entry

			var files []string
			if c.metricsJSON != "" {
				files = append(files, c.metricsJSON)
			}
			// TODO(b/329564182): add other files? e.g. siso_output, siso_trace.json etc.
			if len(files) != 0 {
				var err error
				ents, err = hashFS.Entries(ctx, filepath.Join(execRoot, c.dir), files)
				if err != nil {
					clog.Warningf(ctx, "failed to get entries for %q: %v", files, err)
				}
			}
			ents = append(ents, merkletree.Entry{
				Name: "build.log",
				Data: c.resultstoreUploader.BuildLogData(),
			})
			spin.Start("uploading to resultstore")
			uerr := c.resultstoreUploader.UploadFiles(ctx, ents)
			if uerr != nil {
				clog.Warningf(ctx, "failed to upload results: %v", uerr)
			}
			spin.Stop(uerr)
		}()
	}
	logWriters, done, err := c.initLogWriters(ctx, buildPath)
	if err != nil {
		return stats, err
	}
	defer done(&err)
	bopts, done, err := c.initBuildOpts(ctx, projectID, buildPath, config, ds, hashFS, limits, traceExporter, logWriters)
	if err != nil {
		return stats, err
	}
	defer done(&err)
	spin.Start("loading/recompacting deps log")
	err = eg.Wait()
	spin.Stop(err)
	if localDepsLog != nil {
		defer localDepsLog.Close()
	}
	// TODO(b/286501388): init concurrently for .siso_config/.siso_filegroups, build.ninja.
	if c.fsopt.KeepTainted {
		tainted := hashFS.TaintedFiles()
		if len(tainted) == 0 {
			ui.Default.Warningf(ui.SGR(ui.Yellow, "no tainted generated files:\n"))
		} else if len(tainted) < 5 {
			ui.Default.Warningf(ui.SGR(ui.Yellow, fmt.Sprintf("keep %d tainted files:\n %s\n", len(tainted), strings.Join(tainted, "\n "))))
		} else {
			ui.Default.Warningf(ui.SGR(ui.Yellow, fmt.Sprintf("keep %d tainted files:\n %s\n ...more\n", len(tainted), strings.Join(tainted, "\n "))))
		}
	}

	checkBuildNinja(ctx, c.fname, buildPath, config, hashFS, localDepsLog, bopts)

	spin.Start("load siso config")
	stepConfig, err := ninjabuild.NewStepConfig(ctx, config, buildPath, hashFS, c.fname, c.stateDir)
	if err != nil {
		spin.Stop(err)
		return stats, err
	}
	spin.Stop(nil)
	spin.Start(fmt.Sprintf("load %s", c.fname))
	nstate, err := ninjabuild.Load(ctx, c.fname, buildPath)
	if err != nil {
		spin.Stop(errors.New(""))
		return stats, err
	}
	spin.Stop(nil)

	graph := ninjabuild.NewGraph(ctx, c.fname, nstate, config, buildPath, hashFS, stepConfig, localDepsLog)

	var lastFailedTargets []string
	if c.fastLastFailure && !c.clobber {
		lastFailedTargets, _ = checkTargets(ctx, failedTargetsFilename, targets)
		if len(lastFailedTargets) > 0 {
			bopts.LastFailureTargets = lastFailedTargets
			ui.Default.PrintLines(fmt.Sprintf(ui.SGR(ui.Yellow, "Prioritizing last failed targets: %s\n"), lastFailedTargets))
		}
	}
	err = os.Remove(failedTargetsFilename)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		clog.Warningf(ctx, "failed to remove %s: %v", failedTargetsFilename, err)
	}

	sisoMetadata := SisoMetadata{
		SisoVersion:   c.version,
		StartTime:     c.started,
		BuildID:       c.buildID,
		Targets:       targets,
		MetricsLabels: metricsLabels,
	}
	j, err := json.Marshal(sisoMetadata)
	if err != nil {
		return stats, err
	}
	if err := os.WriteFile(filepath.Join(c.logDir, sisoMetadataFilename), j, 0644); err != nil {
		return stats, err
	}

	return runNinja(ctx, c.fname, graph, bopts, targets, runNinjaOpts{
		cleandead:     c.cleandead,
		subtool:       c.subtool,
		enableStatusz: true,
	})
}

func dumpResourceUsageTable(semaTraces map[string]semaTrace) string {
	var semaNames []string
	for key := range semaTraces {
		semaNames = append(semaNames, key)
	}
	sort.Strings(semaNames)
	var lsb, usb strings.Builder
	var needToShow bool
	ltw := tabwriter.NewWriter(&lsb, 10, 8, 1, ' ', tabwriter.AlignRight)
	utw := tabwriter.NewWriter(&usb, 10, 8, 1, ' ', tabwriter.AlignRight)
	fmt.Fprintf(ltw, "resource/capa\tused(err)\twait-avg\t|   s m |\tserv-avg\t|   s m |\t\n")
	fmt.Fprintf(utw, "resource/capa\tused(err)\twait-avg\t|   s m |\tserv-avg\t|   s m |\t\n")
	for _, key := range semaNames {
		t := semaTraces[key]
		fmt.Fprintf(ltw, "%s\t%d(%d)\t%s\t%s\t%s\t%s\t\n", t.name, t.n, t.nerr, t.waitAvg.Round(time.Millisecond), histogram(t.waitBuckets), t.servAvg.Round(time.Millisecond), histogram(t.servBuckets))
		// bucket 5 = [1m,10m)
		// bucket 6 = [10m,*)
		if t.waitBuckets[5] > 0 || t.waitBuckets[6] > 0 || t.servBuckets[5] > 0 || t.servBuckets[6] > 0 {
			needToShow = true
			fmt.Fprintf(utw, "%s\t%d(%d)\t%s\t%s\t%s\t%s\t\n", t.name, t.n, t.nerr, ui.FormatDuration(t.waitAvg), histogram(t.waitBuckets), ui.FormatDuration(t.servAvg), histogram(t.servBuckets))
		}
	}
	ltw.Flush()
	utw.Flush()
	if needToShow {
		ui.Default.Infof("%s", usb.String())
	}
	return lsb.String()
}

var histchar = [...]string{"▂", "▃", "▄", "▅", "▆", "▇", "█"}

func histogram(b [7]int) string {
	max := 0
	for _, n := range b {
		if max < n {
			max = n
		}
	}
	var sb strings.Builder
	sb.WriteRune('|')
	for _, n := range b {
		if n <= 0 {
			sb.WriteRune(' ')
			continue
		}
		i := len(histchar) * n / (max + 1)
		sb.WriteString(histchar[i])
	}
	sb.WriteRune('|')
	return sb.String()
}

type semaTrace struct {
	name                     string
	n, nerr                  int
	waitAvg, servAvg         time.Duration
	waitBuckets, servBuckets [7]int
}

type dataSource struct {
	cache  cachestore.CacheStore
	client *reapi.Client
}

func (c *Command) initDataSource(ctx context.Context, credential cred.Cred) (dataSource, error) {
	layeredCache := build.NewLayeredCache()
	if c.localCacheEnable {
		cache, err := build.NewLocalCache(c.cacheDir)
		if err != nil {
			clog.Warningf(ctx, "failed to create local cache - no local cache enabled: %v", err)
		} else {
			layeredCache.AddLayer(cache)
			cache.GarbageCollectIfRequired(ctx)
		}
	} else {
		c.cacheDir = ""
	}
	var ds dataSource
	err := c.reopt.CheckValid()
	if err == nil {
		ds.client, err = reapi.New(ctx, credential, *c.reopt)
		if err != nil {
			return ds, err
		}
		layeredCache.AddLayer(ds.client.CacheStore())
	}
	ds.cache = layeredCache
	return ds, nil
}

func (ds dataSource) Close(ctx context.Context) error {
	if ds.client == nil {
		return nil
	}
	return ds.client.Close()
}

func (ds dataSource) DigestData(ctx context.Context, d digest.Digest, fname string) digest.Data {
	return digest.NewData(ds.Source(ctx, d, fname), d)
}

func (ds dataSource) Source(_ context.Context, d digest.Digest, fname string) digest.Source {
	return source{
		dataSource: ds,
		d:          d,
		fname:      fname,
	}
}

type source struct {
	dataSource dataSource
	d          digest.Digest
	fname      string
}

func (s source) Open(ctx context.Context) (io.ReadCloser, error) {
	var r io.ReadCloser
	var err error
	if s.dataSource.cache != nil {
		src := s.dataSource.cache.Source(ctx, s.d, s.fname)
		if src != nil {
			r, err = src.Open(ctx)
			if err == nil {
				return r, nil
			}
		}
		// fallback
	}
	if s.dataSource.client != nil {
		var buf []byte
		buf, err = s.dataSource.client.Get(ctx, s.d, s.fname)
		if err == nil {
			return io.NopCloser(bytes.NewReader(buf)), nil
		}
		// fallback
	}
	// ctx may be deadline exceeded or canceled.
	// if so, return such error.
	// DeadlineExceeded would trigger retry in hashfs flush.
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	// siso process runs at some directory, but
	// s.fname may not be relative to the working directory.
	// Actually, it is exec-root relative if it is created by
	// *Cmd.entriesFromResult, and failed to open as such path
	// doesn't exist. return with better error message.
	if !filepath.IsAbs(s.fname) {
		return nil, fmt.Errorf("failed to fetch source %v for %q: %w", s.d, s.fname, err)
	}
	// no reapi configured. use local file?
	f, err := os.Open(s.fname)
	return f, err
}

func (s source) String() string {
	return fmt.Sprintf("dataSource:%s", s.fname)
}

type lastTargets struct {
	Targets []string `json:"targets,omitempty"`
	Failed  []string `json:"failed,omitempty"`
}

func loadTargets(targetsFile string) ([]string, []string, error) {
	buf, err := os.ReadFile(targetsFile)
	if err != nil {
		return nil, nil, err
	}
	var last lastTargets
	err = json.Unmarshal(buf, &last)
	if err != nil {
		return nil, nil, fmt.Errorf("parse error %s: %w", targetsFile, err)
	}
	return last.Targets, last.Failed, nil
}

func saveTargets(targetsFile string, targets, failed []string) error {
	v := lastTargets{
		Targets: targets,
		Failed:  failed,
	}
	buf, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal last targets: %w", err)
	}
	err = os.WriteFile(targetsFile, buf, 0644)
	if err != nil {
		return fmt.Errorf("save last targets: %w", err)
	}
	return nil
}

func checkTargets(ctx context.Context, lastTargetsFilename string, targets []string) ([]string, bool) {
	lastTargets, failed, err := loadTargets(lastTargetsFilename)
	if err != nil {
		clog.Warningf(ctx, "checkTargets: %v", err)
		return nil, false
	}
	if len(targets) != len(lastTargets) {
		return nil, false
	}
	sort.Strings(targets)
	sort.Strings(lastTargets)
	for i := range targets {
		if targets[i] != lastTargets[i] {
			return nil, false
		}
	}
	return failed, true
}

func argsGN(args, key string) string {
	for line := range strings.SplitSeq(args, "\n") {
		i := strings.Index(line, "#")
		if i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, key) {
			continue
		}
		value := strings.TrimPrefix(line, key)
		value = strings.TrimSpace(value)
		if !strings.HasPrefix(value, "=") {
			continue
		}
		value = strings.TrimPrefix(value, "=")
		return strings.TrimSpace(value)
	}
	return ""
}

func cpuinfo() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "cpu family=%d model=%d stepping=%d ", cpuid.CPU.Family, cpuid.CPU.Model, cpuid.CPU.Stepping)
	fmt.Fprintf(&sb, "brand=%q vendor=%q ", cpuid.CPU.BrandName, cpuid.CPU.VendorString)
	fmt.Fprintf(&sb, "physicalCores=%d threadsPerCore=%d logicalCores=%d ", cpuid.CPU.PhysicalCores, cpuid.CPU.ThreadsPerCore, cpuid.CPU.LogicalCores)
	fmt.Fprintf(&sb, "vm=%t features=%s", cpuid.CPU.VM(), cpuid.CPU.FeatureSet())
	return sb.String()
}

func gcinfo() string {
	var sb strings.Builder
	memoryLimit := debug.SetMemoryLimit(-1) // not adjust the limit, but retrieve current limit
	if memoryLimit == math.MaxInt64 {
		// initial settings
		fmt.Fprintf(&sb, "memory_limit=unlimited ")
	} else {
		fmt.Fprintf(&sb, "memory_limit=%d (GOMEMLIMIT=%s) ", memoryLimit, os.Getenv("GOMEMLIMIT"))
	}

	gcPercent := debug.SetGCPercent(100) // 100 is default
	if gcPercent < 0 {
		ui.Default.PrintLines(ui.SGR(ui.BackgroundRed, fmt.Sprintf("Garbage collection is disabled. GOGC=%s\n", os.Getenv("GOGC"))))
		fmt.Fprintf(&sb, "gc=off")
	} else {
		fmt.Fprintf(&sb, "gc=%d", gcPercent)
	}
	debug.SetGCPercent(gcPercent) // restore original setting
	if v := os.Getenv("GOGC"); v != "" {
		fmt.Fprintf(&sb, " (GOGC=%s)", v)
	}
	return sb.String()
}

func (c *Command) invocation(ctx context.Context, buildID, projectID, execRoot string, properties resultstore.Properties) *rspb.Invocation {
	var username string
	currentUser, err := user.Current()
	if err != nil {
		clog.Warningf(ctx, "failed to get current user: %v", err)
		username = "unknownuser"
	} else {
		username = currentUser.Username
	}
	hostname, err := os.Hostname()
	if err != nil {
		clog.Warningf(ctx, "failed to get hostname: %v", err)
		hostname = "unknownhost"
	}

	return &rspb.Invocation{
		Timing: &rspb.Timing{
			StartTime: timestamppb.New(c.started),
		},
		InvocationAttributes: &rspb.InvocationAttributes{
			ProjectId:   projectID,
			Users:       []string{username},
			Labels:      []string{"siso", "build"},
			Description: fmt.Sprintf("Invocation ID %s", buildID),
		},
		WorkspaceInfo: &rspb.WorkspaceInfo{
			Hostname:         hostname,
			WorkingDirectory: execRoot,
			ToolTag:          "siso",
			CommandLines:     c.commandLines(),
		},
		Properties: properties,
	}
}

func (c *Command) commandLines() []*rspb.CommandLine {
	var cmdlines []*rspb.CommandLine
	cmdlines = append(cmdlines, &rspb.CommandLine{
		Label:   "original",
		Tool:    os.Args[0],
		Args:    os.Args[1:],
		Command: "ninja",
	})
	cmdline := &rspb.CommandLine{
		Label:   "canonical",
		Tool:    os.Args[0],
		Args:    []string{"ninja"},
		Command: "ninja",
	}
	c.Flags.VisitAll(func(f *flag.Flag) {
		cmdline.Args = append(cmdline.Args, fmt.Sprintf("-%s=%s", f.Name, f.Value.String()))
	})
	cmdline.Args = append(cmdline.Args, c.Flags.Args()...)
	cmdlines = append(cmdlines, cmdline)
	return cmdlines
}
