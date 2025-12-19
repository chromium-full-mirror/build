// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninja

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	log "github.com/golang/glog"
	"github.com/google/uuid"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/buildconfig"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/o11y/trace"
	"go.chromium.org/build/siso/reapi"
	"go.chromium.org/build/siso/toolsupport/ninjautil"
	"go.chromium.org/build/siso/ui"
)

// batchFlag implements flag.Value and flag.BoolFlag for the -batch flag.
type batchFlag struct {
	c *Command
}

// IsBoolFlag returns true, indicating that batchFlag is a boolean flag.
func (*batchFlag) IsBoolFlag() bool { return true }

// String returns the string representation of the batch flag's current state.
func (f *batchFlag) String() string {
	if f == nil || f.c == nil {
		return "false"
	}
	// Accessing fields through the embedded struct
	return strconv.FormatBool(!f.c.fastNop && !f.c.fastLocal && !f.c.fastLastFailure && !f.c.fastExit)
}

// Set parses the string value `v` and sets the batch flag's state accordingly.
func (f *batchFlag) Set(v string) error {
	if f == nil || f.c == nil {
		return errors.New("nil batchFlag")
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	// Accessing fields through the embedded struct
	f.c.fastNop = !b
	f.c.fastLocal = !b
	f.c.fastLastFailure = !b
	f.c.fastExit = !b
	return nil
}

// NinjaFlags holds all configuration flags for the ninja command.
type NinjaFlags struct {
	dir        string
	configName string
	projectID  string

	buildID string
	jobID   string

	offline         bool
	fastNop         bool
	fastLocal       bool
	fastLastFailure bool
	fastExit        bool

	quiet           bool
	verbose         bool
	verboseFailures bool

	dryRun          bool
	clobber         bool
	prepare         bool
	strictRemote    bool
	failuresAllowed int
	actionSalt      string

	ninjaJobs      int
	ninjaLoadLimit int

	remoteJobs int
	localJobs  int
	fname      string

	cacheDir         string
	localCacheEnable bool
	cacheEnableRead  bool

	configRepoDir  string
	configFilename string

	outputLocalStrategy string

	depsLogFile string

	stateDir string

	logDir             string
	frontendFile       string
	failureSummaryFile string
	failedCommandsFile string
	outputLogFile      string
	explainFile        string
	localexecLogFile   string
	metricsJSON        string
	traceJSON          string
	buildPprof         string

	fsopt              *hashfs.Option
	reopt              *reapi.Option
	reExecEnable       bool
	reCacheEnableRead  bool
	reCacheEnableWrite bool
	reproxyAddr        string

	artfsDir      string
	artfsEndpoint string

	enableCloudLogging          bool
	enableResultstore           bool
	enableCollector             bool
	collectorAddress            string
	enableCloudProfiler         bool
	cloudProfilerServiceName    string
	enableCloudTrace            bool
	enableCloudMonitoring       bool
	enableBuildNinjaFilesUpload bool
	metricsLabels               string
	metricsProject              string
	traceThreshold              time.Duration
	traceSpanThreshold          time.Duration

	subtool    string
	cleandead  bool
	debugMode  debugMode
	adjustWarn string
}

func (c *Command) SetFlags(flagSet *flag.FlagSet) {
	flagSet.StringVar(&c.dir, "C", ".", "ninja running directory")
	flagSet.StringVar(&c.configName, "config", "", "config name passed to starlark")
	flagSet.StringVar(&c.projectID, "project", os.Getenv("SISO_PROJECT"), "cloud project ID. can set by $SISO_PROJECT")

	defaultBuildID := os.Getenv("SISO_BUILD_ID")
	if defaultBuildID == "" {
		defaultBuildID = uuid.New().String()
	}
	flagSet.StringVar(&c.buildID, "build_id", defaultBuildID, "ID for the build. used for `invocation_id` of remote-apis-sdks and `tool_invocation_id` of remote-apis, and Cloud logging resource `build_id` label.")
	flagSet.StringVar(&c.jobID, "job_id", uuid.New().String(), "ID for a grouping of related builds such as a Buildbucket job. used for `correlated_invocations_id` of remote-apis and remote-apis-sdks, and Cloud logging resource `job_id` label.")

	flagSet.BoolVar(&c.offline, "offline", false, "offline mode.")
	flagSet.BoolVar(&c.offline, "o", false, "alias of `-offline`")
	if f := flagSet.Lookup("offline"); f != nil {
		if s := os.Getenv("RBE_remote_disabled"); s != "" {
			err := f.Value.Set(s)
			if err != nil {
				log.Errorf("invalid RBE_remote_disabled=%q: %v", s, err)
			}
		}
	}

	flagSet.BoolVar(&c.fastNop, "fast_nop", ui.IsTerminal(), "enable fast nop check")
	flagSet.BoolVar(&c.fastLocal, "fast_local", ui.IsTerminal(), "enable fast local")
	flagSet.BoolVar(&c.fastLastFailure, "fast_last_failure", ui.IsTerminal(), "enable fast last failure check")
	flagSet.BoolVar(&c.fastExit, "fast_exit", ui.IsTerminal(), "enable fast exit")
	batch := &batchFlag{c: c}
	flagSet.Var(batch, "batch", "batch mode. prefer thoughput over low latency for build failures. disable -fast_nop, -fast_local -fast_last_failure -fast_exit")

	flagSet.BoolVar(&c.quiet, "quiet", false, "don't show progress status, just command output")
	flagSet.BoolVar(&c.verbose, "verbose", false, "show all command lines while building")
	flagSet.BoolVar(&c.verbose, "v", false, "show all command lines while building (alias of --verbose)")
	flagSet.BoolVar(&c.verboseFailures, "verbose_failures", true, "show failed command lines")
	flagSet.BoolVar(&c.dryRun, "n", false, "dry run")
	flagSet.BoolVar(&c.clobber, "clobber", false, "clobber build")
	flagSet.BoolVar(&c.prepare, "prepare", false, "build inputs of targets, but not build target itself.")
	flagSet.BoolVar(&c.strictRemote, "strict_remote", false, "don't use local for remote step. i.e. no fastlocal, no local fallback")
	flagSet.IntVar(&c.failuresAllowed, "k", 1, "keep going until N jobs fail (0 means inifinity)")
	flagSet.StringVar(&c.actionSalt, "action_salt", "", "action salt")

	flagSet.IntVar(&c.ninjaJobs, "j", -1, "not supported. use -remote_jobs and -local_jobs instead")
	flagSet.IntVar(&c.ninjaLoadLimit, "l", -1, "not supported.")
	flagSet.IntVar(&c.localJobs, "local_jobs", 0, "run N local jobs in parallel. when the value is no positive, the default will be computed based on # of CPUs.")
	flagSet.IntVar(&c.remoteJobs, "remote_jobs", 0, "run N remote jobs in parallel. when the value is no positive, the default will be computed based on # of CPUs.")
	flagSet.StringVar(&c.fname, "f", "build.ninja", "input build manifest filename (relative to -C)")

	flagSet.StringVar(&c.cacheDir, "cache_dir", defaultCacheDir(), "cache directory")
	flagSet.BoolVar(&c.localCacheEnable, "local_cache_enable", false, "local cache enable")
	flagSet.BoolVar(&c.cacheEnableRead, "cache_enable_read", true, "cache enable read")

	flagSet.StringVar(&c.configRepoDir, "config_repo_dir", "build/config/siso", "config repo directory (relative to exec root)")
	flagSet.StringVar(&c.configFilename, "load", "@config//main.star", "config filename (@config// is --config_repo_dir)")
	flagSet.StringVar(&c.outputLocalStrategy, "output_local_strategy", "full", `strategy for output_local. "full": download all outputs. "greedy": downloads most outputs except intermediate objs. "minimum": downloads as few as possible`)
	flagSet.StringVar(&c.depsLogFile, "deps_log", ".siso_deps", "deps log filename (relative to -C, -state_dir)")

	flagSet.StringVar(&c.stateDir, "state_dir", "", "state directory (relative to -C) [default: same dir as build.ninja]")

	flagSet.StringVar(&c.logDir, "log_dir", "", "log directory (relative to -C) [default: same dir as build.ninja]")

	// https://android.googlesource.com/platform/build/soong/+/refs/heads/main/ui/build/ninja.go
	flagSet.StringVar(&c.frontendFile, "frontend_file", "", "frontend FIFO file to report build status to soong ui, or `-` to report to stdout.")

	flagSet.StringVar(&c.failureSummaryFile, "failure_summary", "", "filename for failure summary (relative to -log_dir)")
	c.failedCommandsFile = "siso_failed_commands.sh"
	if runtime.GOOS == "windows" {
		c.failedCommandsFile = "siso_failed_commands.bat"
	}
	flagSet.StringVar(&c.failedCommandsFile, "failed_commands", c.failedCommandsFile, "script file to rerun the last failed commands")
	flagSet.StringVar(&c.outputLogFile, "output_log", "siso_output", "output log filename (relative to -log_dir)")
	flagSet.StringVar(&c.explainFile, "explain_log", "siso_explain", "explain log filename (relative to -log_dir)")
	flagSet.StringVar(&c.localexecLogFile, "localexec_log", "siso_localexec", "localexec log filename (relative to -log_dir)")
	flagSet.StringVar(&c.metricsJSON, "metrics_json", "siso_metrics.json", "metrics JSON filename (relative to -log_dir)")
	flagSet.StringVar(&c.traceJSON, "trace_json", "siso_trace.json", "trace JSON filename (relative to -log_dir)")
	flagSet.StringVar(&c.buildPprof, "build_pprof", "siso_build.pprof", "build pprof filename (relative to -log_dir)")

	c.fsopt = new(hashfs.Option)
	c.fsopt.StateFile = ".siso_fs_state"
	c.fsopt.RegisterFlags(flagSet)

	c.reopt = new(reapi.Option)
	c.reopt.RegisterFlags(flagSet, reapi.Envs("REAPI"))
	flagSet.BoolVar(&c.reExecEnable, "re_exec_enable", true, "remote exec enable")
	flagSet.BoolVar(&c.reCacheEnableRead, "re_cache_enable_read", true, "remote exec cache enable read")
	flagSet.BoolVar(&c.reCacheEnableWrite, "re_cache_enable_write", false, "remote exec cache allow local trusted uploads")
	// reclient_helper.py sets the RBE_server_address
	// https://chromium.googlesource.com/chromium/tools/depot_tools.git/+/e13840bd9a04f464e3bef22afac1976fc15a96a0/reclient_helper.py#138
	c.reproxyAddr = os.Getenv("RBE_server_address")

	flagSet.StringVar(&c.artfsDir, "artfs_dir", "", "artfs mount point")
	flagSet.StringVar(&c.artfsEndpoint, "artfs_endpoint", "localhost:65001", "artfs server endpoint")

	flagSet.DurationVar(&c.traceThreshold, "trace_threshold", 1*time.Minute, "threshold for trace record")
	flagSet.DurationVar(&c.traceSpanThreshold, "trace_span_threshold", 100*time.Millisecond, "theshold for trace span record")

	flagSet.BoolVar(&c.enableCollector, "enable_collector", false, "enable OTEL collector")
	flagSet.StringVar(&c.collectorAddress, "collector_address", "127.0.0.1:4317", "address to dial the collector. Can be path for unix socket unix:///path/to/socket or host:port.")
	flagSet.BoolVar(&c.enableCloudLogging, "enable_cloud_logging", false, "enable cloud logging")
	flagSet.BoolVar(&c.enableResultstore, "enable_resultstore", false, "enable resultstore")
	flagSet.BoolVar(&c.enableCloudProfiler, "enable_cloud_profiler", false, "enable cloud profiler")
	flagSet.StringVar(&c.cloudProfilerServiceName, "cloud_profiler_service_name", "siso", "cloud profiler service name")
	flagSet.BoolVar(&c.enableCloudTrace, "enable_cloud_trace", false, "enable cloud trace")
	flagSet.BoolVar(&c.enableCloudMonitoring, "enable_cloud_monitoring", false, "enable cloud monitoring")
	flagSet.BoolVar(&c.enableBuildNinjaFilesUpload, "enable_build_ninja_files_upload", true, "enable Build Ninja files upload to RBE-CAS")
	flagSet.StringVar(&c.metricsLabels, "metrics_labels", os.Getenv("RBE_metrics_labels"), "comma-separated arbitrary key value pairs in the form key=value, which are added to cloud monitoring metrics and siso_metadata.json.")
	flagSet.StringVar(&c.metricsProject, "metrics_project", os.Getenv("RBE_metrics_project"), "override Cloud Monitoring GCP project where Siso sends action and build metrics.")

	flagSet.StringVar(&c.subtool, "t", "", "run a subtool (use '-t list' to list subtools)")
	flagSet.BoolVar(&c.cleandead, "cleandead", false, "clean built files that are no longer produced by the manifest")
	flagSet.Var(&c.debugMode, "d", "enable debugging (use '-d list' to list modes)")
	flagSet.StringVar(&c.adjustWarn, "w", "", "adjust warnings. not supported b/288807840")
}

func (c *Command) initFlags(targets []string) map[string]string {
	flags := make(map[string]string)
	c.Flags.Visit(func(f *flag.Flag) {
		name := f.Name
		if name == "C" {
			name = "dir"
		}
		flags[name] = f.Value.String()
	})
	flags["project"] = c.projectID
	flags["is_terminal"] = strconv.FormatBool(ui.IsTerminal())
	flags["targets"] = strings.Join(targets, " ")
	return flags
}

func (c *Command) initConfig(ctx context.Context, execRoot string, targets []string) (*buildconfig.Config, error) {
	if c.configFilename == "" {
		return nil, errors.New("no config filename")
	}
	cfgrepos := map[string]fs.FS{
		"config":           os.DirFS(c.configRepoDir),
		"config_overrides": os.DirFS(filepath.Join(execRoot, ".siso_remote")),
	}
	flags := c.initFlags(targets)
	config, err := buildconfig.New(ctx, c.configFilename, flags, cfgrepos)
	if err != nil {
		return nil, err
	}
	if gnArgs, err := os.ReadFile("args.gn"); err == nil {
		err := config.Metadata.Set("args.gn", string(gnArgs))
		if err != nil {
			return nil, err
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		clog.Warningf(ctx, "no args.gn: %v", err)
	} else {
		return nil, err
	}
	return config, nil
}

func (c *Command) initWorkdirs(ctx context.Context) (string, error) {
	// don't use $PWD for current directory
	// to avoid symlink issue. b/286779149
	pwd := os.Getenv("PWD")
	_ = os.Unsetenv("PWD") // no error for safe env key name.

	execRoot, err := os.Getwd()
	if pwd != "" {
		_ = os.Setenv("PWD", pwd) // no error to reset env with valid value.
	}
	if err != nil {
		return "", err
	}
	c.startDir = execRoot
	clog.Infof(ctx, "wd: %s", execRoot)
	// The formatting of this string, complete with funny quotes, is
	// so Emacs can properly identify that the cwd has changed for
	// subsequent commands.
	// Don't print this if a tool is being used, so that tool output
	// can be piped into a file without this string showing up.
	if c.subtool == "" && c.dir != "." {
		ui.Default.PrintLines(fmt.Sprintf("ninja: Entering directory `%s'\n\n", c.dir))
	}
	err = os.Chdir(c.dir)
	if err != nil {
		return "", err
	}
	clog.Infof(ctx, "change dir to %s", c.dir)
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	realCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		clog.Warningf(ctx, "failed to eval symlinks %q: %v", cwd, err)
	} else if cwd != realCWD {
		clog.Infof(ctx, "cwd %s -> %s", cwd, realCWD)
		cwd = realCWD
	}
	if !filepath.IsAbs(c.configRepoDir) {
		execRoot, err = build.DetectExecRoot(cwd, c.configRepoDir)
		if err != nil {
			return "", err
		}
		c.configRepoDir = filepath.Join(execRoot, c.configRepoDir)
	}
	clog.Infof(ctx, "exec_root: %s", execRoot)

	// recalculate dir as relative to exec_root.
	// recipe may use absolute path for -C.
	rdir, err := filepath.Rel(execRoot, cwd)
	if err != nil {
		return "", err
	}
	if !filepath.IsLocal(rdir) {
		return "", fmt.Errorf("dir %q is out of exec root %q", cwd, execRoot)
	}
	c.dir = rdir
	clog.Infof(ctx, "working_directory in exec_root: %s", c.dir)
	if c.startDir != execRoot {
		ui.Default.Infof("exec_root=%s dir=%s\n", execRoot, c.dir)
	}
	_, err = os.Stat(c.fname)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s not found in %s. need `-C <dir>`?", c.fname, cwd)
	}
	return execRoot, err
}

func initDepsLog(ctx context.Context, stateDir string, depsLogFile string) (*ninjautil.DepsLog, error) {
	depsLogPath := filepath.Join(stateDir, depsLogFile)
	err := os.MkdirAll(filepath.Dir(depsLogPath), 0755)
	if err != nil {
		clog.Warningf(ctx, "failed to mkdir for deps log: %v", err)
		return nil, err
	}
	depsLog, err := ninjautil.NewDepsLog(ctx, depsLogPath)
	if err != nil {
		clog.Warningf(ctx, "failed to load deps log: %v", err)
		return nil, err
	}
	if !depsLog.NeedsRecompact() {
		return depsLog, nil
	}
	err = depsLog.Recompact(ctx)
	if err != nil {
		clog.Warningf(ctx, "failed to recompact deps log: %v", err)
		return nil, err
	}
	return depsLog, nil
}

func (c *Command) initBuildOpts(ctx context.Context, projectID string, buildPath *build.Path, config *buildconfig.Config, ds dataSource, hashFS *hashfs.HashFS, limits build.Limits, traceExporter *trace.Exporter, logWriters logWriters) (bopts build.Options, done func(*error), err error) {
	var dones []func(*error)
	defer func() {
		if err != nil {
			for i := len(dones) - 1; i >= 0; i++ {
				dones[i](&err)
			}
			dones = nil
		}
	}()
	if !filepath.IsAbs(c.traceJSON) {
		c.traceJSON = filepath.Join(c.logDir, c.traceJSON)
	}
	if !filepath.IsAbs(c.buildPprof) {
		c.buildPprof = filepath.Join(c.logDir, c.buildPprof)
	}

	var actionSaltBytes []byte
	if c.actionSalt != "" {
		actionSaltBytes = []byte(c.actionSalt)
	}
	if c.traceJSON != "" {
		rotateFiles(ctx, c.traceJSON)
	}

	cache, err := build.NewCache(ctx, build.CacheOptions{
		Store:      ds.cache,
		EnableRead: c.cacheEnableRead,
	})
	if err != nil {
		clog.Warningf(ctx, "no cache enabled: %v", err)
	}
	bopts = build.Options{
		JobID:                 c.jobID,
		ID:                    c.buildID,
		StartTime:             c.started,
		ProjectID:             projectID,
		Metadata:              config.Metadata,
		Path:                  buildPath,
		HashFS:                hashFS,
		REAPIClient:           ds.client,
		REExecEnable:          c.reExecEnable,
		RECacheEnableRead:     c.reCacheEnableRead,
		RECacheEnableWrite:    c.reCacheEnableWrite,
		ReproxyAddr:           c.reproxyAddr,
		ActionSalt:            actionSaltBytes,
		OutputLocal:           build.OutputLocalFunc(c.fsopt.OutputLocal),
		Cache:                 cache,
		FailureSummaryWriter:  logWriters.failureSummaryWriter,
		FailedCommandsWriter:  logWriters.failedCommandsWriter,
		OutputLogWriter:       logWriters.outputLogWriter,
		ExplainWriter:         logWriters.explainWriter,
		LocalexecLogWriter:    logWriters.localexecLogWriter,
		MetricsJSONWriter:     logWriters.metricsJSONWriter,
		TraceExporter:         traceExporter,
		TraceJSON:             c.traceJSON,
		Pprof:                 c.buildPprof,
		ResultstoreUploader:   c.resultstoreUploader,
		Clobber:               c.clobber,
		FastExit:              c.fastExit,
		Prepare:               c.prepare,
		Verbose:               c.verbose,
		VerboseFailures:       c.verboseFailures,
		DryRun:                c.dryRun,
		StrictRemote:          c.strictRemote,
		FailuresAllowed:       c.failuresAllowed,
		KeepRSP:               c.debugMode.Keeprsp,
		KeepDepfile:           c.debugMode.Keepdepfile,
		Limits:                limits,
		UploadBuildNinjaFiles: c.enableBuildNinjaFilesUpload,
	}
	return bopts, func(err *error) {
		for i := len(dones) - 1; i >= 0; i-- {
			dones[i](err)
		}
	}, nil
}

func defaultCacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		log.Warningf("Failed to get user cache dir: %v", err)
		return ""
	}
	return filepath.Join(d, "siso")
}

func initOutputLocal(outputLocalStrategy string) (func(context.Context, string) bool, error) {
	switch outputLocalStrategy {
	case "full":
		return func(context.Context, string) bool { return true }, nil
	case "greedy":
		return func(ctx context.Context, fname string) bool {
			// Note: d. wil be downloaded to get deps anyway,
			// but will not be written to disk.
			switch filepath.Ext(fname) {
			case ".o", ".obj", ".a", ".d", ".stamp":
				return false
			}
			return true
		}, nil
	case "minimum":
		return func(ctx context.Context, fname string) bool {
			// force to output local for inputs
			// .h,/.hxx/.hpp/.inc/.c/.cc/.cxx/.cpp/.m/.mm for gcc deps or msvc showIncludes
			// .json/.js/.ts for tsconfig.json, .js for grit etc.
			// .py for protobuf py etc.
			switch filepath.Ext(fname) {
			case ".h", ".hxx", ".hpp", ".inc", ".c", ".cc", "cxx", ".cpp", ".m", ".mm", ".json", ".js", ".ts", ".py":
				return true
			}
			return false
		}, nil
	default:
		return nil, fmt.Errorf("unknown output local strategy: %q. should be full/greedy/minimum", outputLocalStrategy)
	}
}
