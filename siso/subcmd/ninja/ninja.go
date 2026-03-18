// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package ninja implements the subcommand `ninja` which parses a `build.ninja` file and builds the requested targets.
package ninja

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"go/version"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	log "github.com/golang/glog"
	"github.com/google/subcommands"
	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.chromium.org/build/siso/auth/cred"
	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/ninjabuild"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/o11y/monitoring"
	"go.chromium.org/build/siso/o11y/trace"
	"go.chromium.org/build/siso/reapi"
	"go.chromium.org/build/siso/signals"
	"go.chromium.org/build/siso/toolsupport/artfsutil"
	"go.chromium.org/build/siso/toolsupport/cogutil"
	"go.chromium.org/build/siso/toolsupport/soongutil"
	"go.chromium.org/build/siso/ui"
)

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
	localCacheOptions

	sisoInfoLog string // abs or relative to logDir
	startDir    string
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
		ui.Default = quietUI{
			heartbeatPeriod: c.heartbeatPeriod,
		}
		spin := ui.Default.NewSpinner()
		spin.Start("")
		defer spin.Stop(nil)
	} else if c.frontendFile != "" {
		f := os.Stdout
		if c.frontendFile != "-" {
			f, err = os.OpenFile(c.frontendFile, os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				ui.Default.Errorf("failed to open frontend file: %v\n", err)
				return subcommands.ExitFailure
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

	stats, err := c.Run(ctx)
	return c.postRun(stats, err)
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

func (c *Command) setup(ctx context.Context) (buildPath *build.Path, doneLock func(), resetCrashOutput func(), err error) {
	err = c.resolveFlags()
	if err != nil {
		return nil, nil, nil, err
	}
	if c.offline {
		c.enableOfflineMode(ctx)
	}

	buildPath, err = c.changeToWorkdir(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	if c.stateDir != "." && c.fsopt.StateFile != "" {
		c.fsopt.StateFile = filepath.Join(c.stateDir, c.fsopt.StateFile)
	}
	doneLock, err = initLock(ctx, c.dryRun, c.stateDir)
	if err != nil {
		return nil, nil, nil, err
	}

	err = c.initLogDir(ctx)
	if err != nil {
		doneLock()
		return nil, nil, nil, err
	}
	clog.Infof(ctx, "siso log dir=%s", c.logDir)

	resetCrashOutput, err = c.setupCrashOutput(ctx)
	if err != nil {
		doneLock()
		return nil, nil, nil, err
	}
	return buildPath, doneLock, resetCrashOutput, nil
}

func (c *Command) computeLimits(ctx context.Context) build.Limits {
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
	if !c.fastLocal && (limits.FastLocal != 0 || limits.StartLocal != 0) && (limits.Remote > 0 || limits.REWrap > 0) {
		needWarn := true
		c.Flags.Visit(func(f *flag.Flag) {
			if f.Name == "fast_local" {
				needWarn = false
			}
			if f.Name == "frontend_file" {
				needWarn = false
			}
		})
		if needWarn {
			var changes []string
			if limits.FastLocal != 0 {
				changes = append(changes, fmt.Sprintf("fastlocal=%d->0", limits.FastLocal))
			}
			if limits.StartLocal != 0 {
				changes = append(changes, fmt.Sprintf("startlocal=%d->0", limits.StartLocal))
			}
			ui.Default.Warningf("%s", ui.SGR(ui.Yellow, fmt.Sprintf("disable fast local for non-interactive: %s\n use `--fast_local` to enable fast local with non-interactive mode\n",
				strings.Join(changes, " "))))
		}
		clog.Infof(ctx, "disable fastlocal, startlocal")
		limits.FastLocal = 0
		limits.StartLocal = 0
	}
	c.checkResourceLimits(ctx, limits)
	return limits
}

func (c *Command) initCredentials(ctx context.Context) (cred.Cred, error) {
	needsCreds := !c.offline && (c.reopt.NeedCred() || c.enableCloudLogging || c.enableResultstore || c.enableCloudProfiler || c.enableCloudTrace || c.enableCloudMonitoring)
	if !needsCreds {
		return cred.Cred{}, nil
	}

	// TODO: can be async until cred is needed?
	spin := ui.Default.NewSpinner()
	spin.Start("init credentials by %q", c.authOpts.Type)
	credential, err := cred.New(ctx, c.reopt.ServiceURI(), c.authOpts)
	if err != nil {
		spin.Stop(errors.New(""))
		return cred.Cred{}, err
	}
	spin.Stop(nil)
	return credential, nil
}

// Exposed for e2e testing. To be reevaluated.
func (c *Command) Run(ctx context.Context) (stats build.Stats, finalErr error) {
	if runtime.GOOS == "darwin" && version.Compare(runtime.Version(), "go1.26") >= 0 {
		// If go1.26.0+ is used, check greenteagc is disabled.
		// See https://github.com/golang/go/issues/77824
		enableGreenTeaGC := true

		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "GOEXPERIMENT" && setting.Value == "nogreenteagc" {
					enableGreenTeaGC = false
				}
			}
		} else {
			fmt.Fprintf(os.Stderr, "failed to read build info\n")
			os.Exit(1)
		}

		if enableGreenTeaGC {
			fmt.Fprintf(os.Stderr, "siso must be built with GOEXPERIMENT=nogreenteagc on darwin when using go1.26.0 or later.\n")
			os.Exit(1)
		}
	}

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

	buildPath, doneLock, resetCrashOutput, err := c.setup(ctx)
	if err != nil {
		return stats, err
	}
	defer doneLock()
	defer resetCrashOutput()

	limits := c.computeLimits(ctx)
	projectID := c.reopt.UpdateProjectID(c.projectID)

	credential, err := c.initCredentials(ctx)
	if err != nil {
		return stats, err
	}
	if c.enableCloudLogging {
		spin := ui.Default.NewSpinner()
		spin.Start("init cloud logging")
		logCtx, loggerURL, done, err := c.initCloudLogging(ctx, projectID, buildPath.ExecRoot, credential)
		spin.Stop(err)
		if err != nil {
			// b/335295396 Compile step hitting write requests quota
			// rather than build fails, fallback to glog.
			ui.Default.Warningf("fallback to glog\n")
			c.enableCloudLogging = false
		} else {
			// use stderr for confirm no-op step. b/288534744
			ui.Default.Infof("%s\n", loggerURL)
			pCleanups = append(pCleanups, done)
			ctx = logCtx
		}
	}
	clog.Infof(ctx, "siso version %s", c.version)
	// logging is ready.
	properties := c.buildProperties(ctx, buildPath)
	// log build properties
	for _, p := range properties {
		clog.Infof(ctx, "%s: %q", p.Key, p.Value)
	}
	clog.Infof(ctx, "job id: %q", c.jobID)
	clog.Infof(ctx, "build id: %q", c.buildID)
	clog.Infof(ctx, "project id: %q", projectID)
	clog.Infof(ctx, "commandline %q", os.Args)
	clog.Infof(ctx, "is_terminal=%t fast_nop=%t fast_local=%t fast_last_failure=%t fast_exit=%t", ui.IsTerminal(), c.fastNop, c.fastLocal, c.fastLastFailure, c.fastExit)

	spin := ui.Default.NewSpinner()
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
			isErr := finalErr != nil && !errors.Is(finalErr, errNothingToDo)
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
	config, err := c.initConfig(ctx, buildPath.ExecRoot, targets)
	if err != nil {
		return stats, err
	}

	var eg errgroup.Group
	var localDepsLog *ninjabuild.DepsLog
	eg.Go(func() error {
		depsLog, err := initDepsLog(ctx, c.stateDir, c.depsLogFile)
		if err != nil {
			return err
		}
		localDepsLog = depsLog
		return nil
	})

	var reapiClient *reapi.Client
	if err := c.reopt.CheckValid(); err == nil {
		ui.Default.Infof("use %s\n", c.reopt)
		reapiClient, err = reapi.New(ctx, credential, *c.reopt)
		if err != nil {
			return stats, err
		}
		eg.Go(func() error {
			err := reapiClient.Init(ctx)
			if err != nil {
				return err
			}
			if c.reExecEnable {
				err := reapiClient.CheckWritable(ctx)
				if err != nil {
					return err
				}
			}
			return nil
		})
	} else {
		if c.strictRemote {
			return stats, flagError{err: fmt.Errorf("no reapi specified, but remote is requested as --strict_remote: %w", err)}
		}
		if c.remoteJobs > 0 && c.reproxyAddr == "" {
			return stats, flagError{err: fmt.Errorf("no reapi specified, but remote is requested as --remote_jobs=%d: %w", c.remoteJobs, err)}
		}
	}
	if !c.localCacheEnable {
		c.cacheDir = ""
	}

	ds := build.NewDataSource(ctx, credential, c.localCacheEnable, c.cacheDir, reapiClient)
	defer func() {
		err := ds.Close(ctx)
		if err != nil {
			clog.Errorf(ctx, "close datasource: %v", err)
		}
	}()
	hashFS, closeHashFS, err := c.setupHashFS(ctx, buildPath, ds)
	if err != nil {
		return stats, err
	}
	defer func() { c.saveFailedTargetsAndCommand(ctx, finalErr, targets) }()
	defer func() { closeHashFS(targets, finalErr) }()
	hashFSErr := hashFS.LoadErr()
	if hashFSErr != nil {
		ui.Default.Errorf("%s", ui.SGR(ui.BackgroundRed, fmt.Sprintf("unable to do incremental build as fs state is corrupted: %v\n", hashFSErr)))
	}

	lastFailed := hasLastFailedTargets(c.stateDir)
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

	if c.enableResultstore {
		cleanup, err := c.setupResultStore(ctx, projectID, buildPath, properties, credential, hashFS)
		if err != nil {
			return stats, err
		}
		defer func() { cleanup(finalErr) }()
	}

	logWriters, done, err := c.initLogWriters(ctx, buildPath)
	if err != nil {
		return stats, err
	}
	// It mutates finalErr, hence passing over pointer.
	defer done(&finalErr)
	bopts := c.initBuildOpts(ctx, projectID, buildPath, config, ds, hashFS, limits, traceExporter, logWriters)
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
			ui.Default.Warningf("%s", ui.SGR(ui.Yellow, "no tainted generated files:\n"))
		} else if len(tainted) < 5 {
			ui.Default.Warningf("%s", ui.SGR(ui.Yellow, fmt.Sprintf("keep %d tainted files:\n %s\n", len(tainted), strings.Join(tainted, "\n "))))
		} else {
			ui.Default.Warningf("%s", ui.SGR(ui.Yellow, fmt.Sprintf("keep %d tainted files:\n %s\n ...more\n", len(tainted), strings.Join(tainted, "\n "))))
		}
	}

	err = ninjabuild.CheckManifest(ctx, c.fname, buildPath, config, hashFS, localDepsLog, &bopts)
	if err != nil {
		return stats, err
	}

	spin.Start("load siso config")
	stepConfig, err := ninjabuild.NewStepConfig(ctx, config, buildPath, c.fname, c.stateDir)
	if err != nil {
		spin.Stop(err)
		return stats, err
	}
	spin.Stop(nil)
	spin.Start("load %s", c.fname)
	nstate, err := ninjabuild.Load(ctx, c.fname, buildPath)
	if err != nil {
		spin.Stop(errors.New(""))
		return stats, err
	}
	spin.Stop(nil)

	graph := ninjabuild.NewGraph(ctx, c.fname, nstate, config, buildPath, hashFS, stepConfig, localDepsLog)

	// Set last failure targets if necessary, and remove the last failed targets file unconditionally.
	if c.fastLastFailure && !c.clobber {
		lastFailedTargets := loadLastFailedTargets(ctx, c.stateDir, targets)
		if len(lastFailedTargets) > 0 {
			bopts.LastFailureTargets = lastFailedTargets
			ui.Default.PrintLines(fmt.Sprintf(ui.SGR(ui.Yellow, "Prioritizing last failed targets: %s\n"), lastFailedTargets))
		}
	}
	removeLastFailedTargets(ctx, c.stateDir)

	err = c.writeSisoMetadata(metricsLabels, targets)
	if err != nil {
		return stats, err
	}

	return ninjabuild.Run(ctx, graph, bopts, targets, ninjabuild.RunNinjaOpts{
		Cleandead:     c.cleandead,
		Subtool:       c.subtool,
		EnableStatusz: true,
	})
}

// postRun prints build result messages and returns exit status based on the build stats and the error from Run().
func (c *Command) postRun(stats build.Stats, runErr error) subcommands.ExitStatus {
	d := time.Since(c.started)
	sps := float64(stats.Done-stats.Skipped) / d.Seconds()
	dur := ui.FormatDuration(d)
	if runErr != nil {
		var errFlag flagError
		var errBuild ninjabuild.BuildError
		switch {
		case errors.Is(runErr, errNothingToDo):
			msgPrefix := "Everything is up-to-date"
			if ui.IsTerminal() {
				msgPrefix = ui.SGR(ui.Green, msgPrefix)
			}
			ui.Default.Infof("%s Nothing to do.\n", msgPrefix)
			return subcommands.ExitSuccess

		case errors.As(runErr, &errFlag):
			ui.Default.Errorf("%v\n", runErr)

		case errors.As(runErr, &errBuild):
			var errTarget build.TargetError
			if errors.As(errBuild.Err, &errTarget) {
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
				return subcommands.ExitFailure
			}
			var errMissingSource build.MissingSourceError
			if errors.As(errBuild.Err, &errMissingSource) {
				msgPrefix := "Schedule Failure"
				if ui.IsTerminal() {
					dur = ui.SGR(ui.Bold, dur)
					msgPrefix = ui.SGR(ui.BackgroundRed, msgPrefix)
				}
				ui.Default.Errorf("\n%6s %s: %v\n", dur, msgPrefix, errMissingSource)
				return subcommands.ExitFailure
			}
			msgPrefix := "Build Failure"
			if ui.IsTerminal() {
				dur = ui.SGR(ui.Bold, dur)
				msgPrefix = ui.SGR(ui.BackgroundRed, msgPrefix)
			}
			ui.Default.Errorf("\n%6s %s: %d done, %d failed, %d remaining - %.02f/s\n %v\n", dur, msgPrefix, stats.Done-stats.Skipped, stats.Fail, stats.Total-stats.Done, sps, errBuild.Err)
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
			if status.Code(runErr) == codes.Unavailable {
				ui.Default.Errorf("\n%6s %s: could not connect to backend. If you want to build offline, pass `-o` or `--offline`\n %v\n", ui.FormatDuration(time.Since(c.started)), msgPrefix, runErr)
			} else {
				ui.Default.Errorf("\n%6s %s: %v\n", ui.FormatDuration(time.Since(c.started)), msgPrefix, runErr)
			}
		}
		return subcommands.ExitFailure
	}
	msgPrefix := "Build Succeeded"
	if ui.IsTerminal() {
		dur = ui.SGR(ui.Bold, dur)
		msgPrefix = ui.SGR(ui.Green, msgPrefix)
	}
	ui.Default.Infof("%6s %s: %d steps - %.02f/s\n", dur, msgPrefix, stats.Done-stats.Skipped, sps)
	return subcommands.ExitSuccess

}

func (c *Command) saveFailedTargetsAndCommand(ctx context.Context, err error, targets []string) {
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
		var errBuild ninjabuild.BuildError
		if !errors.As(err, &errBuild) {
			return
		}
		var stepError build.StepError
		if !errors.As(errBuild.Err, &stepError) {
			rerr := os.Remove(c.logFilename(c.failedCommandsFile, ""))
			if rerr != nil {
				clog.Warningf(ctx, "failed to remove failed command file: %v", rerr)
			}
			return
		}
		// store failed targets only when build steps failed.
		// i.e., don't store with error like context canceled, etc.
		clog.Infof(ctx, "record failed targets: %q", stepError.Target)
		serr := saveLastFailedTargets(c.stateDir, targets, []string{stepError.Target})
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
}

func (c *Command) setupHashFS(ctx context.Context, buildPath *build.Path, ds build.DataSource) (*hashfs.HashFS, func([]string, error), error) {
	c.fsopt.DataSource = ds
	var err error
	c.fsopt.OutputLocal, err = initOutputLocal(c.outputLocalStrategy)
	if err != nil {
		return nil, nil, err
	}
	if c.logDir == "." || c.logDir == filepath.Join(buildPath.ExecRoot, buildPath.Dir) {
		cwd := filepath.Join(buildPath.ExecRoot, buildPath.Dir)
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
		ninjaLogFname := filepath.Join(buildPath.ExecRoot, buildPath.Dir, ".ninja_log")
		c.fsopt.Ignore = func(ctx context.Context, fname string) bool {
			return fname == ninjaLogFname
		}
	}
	cogfs, err := cogutil.New(ctx, buildPath.ExecRoot)
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
			return nil, nil, err
		}
		ui.Default.PrintLines(ui.SGR(ui.Yellow, "build on artfs\n"))
		c.fsopt.ArtFS = artfs
	}

	c.fsopt.FSMonitor = initFSMonitor(ctx, buildPath.ExecRoot)

	spin := ui.Default.NewSpinner()
	spin.Start("loading fs state")

	hashFS, err := hashfs.New(ctx, *c.fsopt)
	spin.Stop(err)
	if err != nil {
		return nil, nil, err
	}
	close := func(targets []string, err error) {
		shouldSetTargets := !c.dryRun && c.subtool == "" && !c.prepare && err == nil
		hashFS.SetBuildTargets(ctx, targets, shouldSetTargets)
		cerr := hashFS.Close(ctx)
		if cerr != nil {
			clog.Errorf(ctx, "close hashfs: %v", cerr)
		}
	}
	return hashFS, close, nil
}
