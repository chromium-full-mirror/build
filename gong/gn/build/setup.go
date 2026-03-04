// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package build builds a graph of GN targets based on an invocation.
package build

import (
	"fmt"
	"iter"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"go.chromium.org/build/gong/gn"
	"go.chromium.org/build/gong/gn/build/analysis"
	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

const (
	gnFile           = ".gn"
	buildArgFileName = "args.gn"
)

func findDotFile(currentDir string) (string, error) {
	tryThisFile := filepath.Join(currentDir, gnFile)
	if _, err := os.Stat(tryThisFile); err == nil {
		return tryThisFile, nil
	}
	upOneDir := filepath.Dir(currentDir)
	if upOneDir == currentDir {
		// Got to the top.
		return "", os.ErrNotExist
	}
	return findDotFile(upOneDir)
}

// Setup is helper to set up the build settings and environment for the various
// commands to run.
type Setup struct {
	BuildSettings    environment.BuildSettings
	loader           analysis.Loader
	builder          analysis.Builder
	rootBuildFile    fs.SourceFile
	inputFileManager fs.InputFileManager

	// FillArguments sets whether the build arguments should be filled during setup from the
	// command line/build argument file. This will be true by default. The use
	// case for setting it to false is when editing build arguments, we don't
	// want to rely on them being valid.
	FillArguments bool

	// Settings object for interpreting the .gn config file, and build arguments
	// from either the command line or build argument file.
	dotfileSettings *analysis.Settings
	// Scope object used to interpret the .gn config file.
	// (This is separate from dotfileSettings because build arguments should not be
	// able to reference variables defined in the root config file.)
	dotfileScope *resolve.Scope
	// State for invoking the dotfile.
	dotfileName string
}

// NewSetup creates a new Setup helper.
func NewSetup() *Setup {
	setup := &Setup{
		FillArguments: true,
	}
	setup.loader = analysis.MakeLoader(&setup.BuildSettings, &setup.inputFileManager)
	setup.builder = analysis.MakeBuilder(&setup.loader)
	setup.dotfileSettings = analysis.NewSettings(&setup.BuildSettings)
	setup.dotfileScope = setup.dotfileSettings.NewScope()
	return setup
}

// DoSetup configures the build for the current command line.
func (s *Setup) DoSetup(buildDir string, forceCreate bool, flags *gn.CommonFlags) error {
	if flags.Time || flags.Tracelog != "" {
		fmt.Fprintf(os.Stderr, "tracing not yet implemented")
	}

	if err := s.FillSourceDir(flags); err != nil {
		return err
	}
	if err := s.RunConfigFile(); err != nil {
		return err
	}
	if err := s.fillOtherConfig(); err != nil {
		return err
	}

	// Must be after FillSourceDir to resolve.
	if err := s.fillBuildDir(buildDir); err != nil {
		return err
	}

	if s.FillArguments {
		if err := s.fillArguments(flags); err != nil {
			fmt.Fprintf(os.Stderr, "don't know how to fill args yet, skipping for now: %v\n", err)
		}
	}
	if err := s.fillPythonPath(flags); err != nil {
		return err
	}

	// Check for unused variables in the .gn file.
	if err := s.dotfileScope.CheckForUnusedVars(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "warn: DoSetup not completely implemented\n")
	return nil
}

func (s *Setup) fillArguments(flags *gn.CommonFlags) error {
	// TODO: implement properly
	if flags.Args != "" {
		return fmt.Errorf("don't know how to parse args from command line yet")
	}

	argsInputPath := path.Join(s.BuildSettings.BuildDir.Path(), buildArgFileName)
	argsInputFile, err := fs.NewInputFile(argsInputPath, argsInputPath)
	if err != nil {
		return fmt.Errorf("could not load args file: %w", err)
	}
	// TODO: retrieve the binary name?
	argsInputFile.FriendlyName = `build arg file (use "gn args <out_dir>" to edit)`

	argsTokens, err := syntax.Tokenize(argsInputFile)
	if err != nil {
		return fmt.Errorf("args tokenize failed: %w", err)
	}

	argsRoot, err := parse.Parse(argsTokens)
	if err != nil {
		return fmt.Errorf("args parse failed: %w", err)
	}

	argScope := s.dotfileSettings.NewScope()
	_, err = resolve.ExecuteNode(argsRoot, argScope)
	if err != nil {
		return fmt.Errorf("args execute failed: %w", err)
	}

	// TODO: do something with the resulting scope

	return nil
}

// FillSourceDir fills the root directory into the settings.
func (s *Setup) FillSourceDir(flags *gn.CommonFlags) error {
	// Find the .gn file.
	var rootPath string

	// Prefer the command line args to the config file.
	if flags.Root != "" {
		var err error
		rootPath, err = filepath.Abs(flags.Root)
		if err != nil {
			return fmt.Errorf("root source path not found: %w", err)
		}

		// When --root is specified, an alternate --dotfile can also be set.
		// --dotfile should be a real file path and not a "//foo" source-relative
		// path.
		if flags.Dotfile == "" {
			s.dotfileName = filepath.Join(rootPath, gnFile)
		} else {
			s.dotfileName, err = filepath.Abs(flags.Dotfile)
			if err != nil {
				return fmt.Errorf("could not find dotfile: %w", err)
			}
			// Only set DotfileName if it was passed explicitly.
			s.BuildSettings.DotfileName = s.dotfileName
		}
	} else {
		// In the default case, look for a dotfile and that also tells us where the
		// source root is.
		currentDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("could not get current directory: %w", err)
		}
		s.dotfileName, err = findDotFile(currentDir)
		if err != nil {
			return fmt.Errorf("can't find source root: %w", err)
		}
		rootPath = filepath.Dir(s.dotfileName)
	}

	rootRealpath, err := filepath.Abs(rootPath)
	if err != nil {
		return fmt.Errorf("can't get the real root path of %s: %w", rootPath, err)
	}
	s.BuildSettings.RootPath = rootRealpath

	return nil
}

func (s *Setup) fillBuildDir(buildDir string) error {
	// Figure out where the user is right now, relative to the source root.
	var currentContext fs.SourceDir
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}
	currentContext, err = fs.MakeSourceDirFromPath(s.BuildSettings.RootPath, wd)
	if err != nil {
		return fmt.Errorf("failed to make SourceDir from wd: %w", err)
	}

	// This lets us resolve the build dir they specify relative to their wd.
	// e.g. if they're in //foo/bar then we can resolve ../../out/Default correctly.
	// However, at this point symlinks have not been evaluated.
	resolved, err := currentContext.ResolveRelativeDir(buildDir)
	if err != nil {
		return fmt.Errorf("failed to resolve build dir from wd: %w", err)
	}

	// Create the build dir.
	buildDirAbs := s.BuildSettings.FullDirPath(resolved)
	if err := os.MkdirAll(buildDirAbs, 0755); err != nil {
		return fmt.Errorf("failed to create build directory: %w", err)
	}

	// Now that it's created, evaluate symlinks to get the real path.
	buildDirReal, err := filepath.EvalSymlinks(buildDirAbs)
	if err != nil {
		return fmt.Errorf("failed to get real build dir path: %w", err)
	}

	// Reevaluate the SourceDir from the real path.
	resolved, err = fs.MakeSourceDirFromPath(s.BuildSettings.RootPath, buildDirReal)
	if err != nil {
		return fmt.Errorf("failed to make SourceDir from real build dir path: %w", err)
	}

	s.BuildSettings.BuildDir = resolved
	return nil
}

func (s *Setup) fillPythonPath(flags *gn.CommonFlags) error {
	// TODO(b/388723392): Need parity with C++ GN's Windows handling
	// https://source.chromium.org/gn/gn/+/main:src/gn/setup.cc;l=791-825;drc=81dab9f25cb2381400c237fdea7030d5068f9a73
	// Maybe this can be resolved using exec.LookPath?
	// https://pkg.go.dev/os/exec?GOOS=windows#LookPath
	if runtime.GOOS == "windows" {
		fmt.Fprintf(os.Stderr, "WARNING: python path detection will not function as expected on windows\n")
	}

	// Command line takes precedence.
	if flags.ScriptExecutable != "" {
		// TODO(b/388723392): C++ GN uses a function here GetSwitchValueNative
		// https://source.chromium.org/search?q=GetSwitchValueNative&sq=&ss=gn
		// to ensure only the last flag is used, but calling this is done where
		// the flags are used rather than processing them all in advance.
		// This raises the risk of behavioral incompatibility with C++ GN.
		// May need to look into this issue further?
		s.BuildSettings.PythonPath = flags.ScriptExecutable
		return nil
	}

	// Use `script_executable` from the dotfile if available.
	if value := s.dotfileScope.Value("script_executable", true); value != nil {
		stringValue, err := resolve.AsValue[*resolve.StringValue](value)
		if err != nil {
			return err
		}
		s.BuildSettings.PythonPath = stringValue.String()
		return nil
	}

	// Fallback to Python from PATH.
	// (Yes, this is a line of code referencing "python" in 2025 because that's
	// what C++ GN does, and you're probably going to want to override that
	// manually with `script_executable = "python3"` in your .gn file like these:
	// https://source.chromium.org/search?q=script_executable)
	s.BuildSettings.PythonPath = "python"
	return nil
}

// RunConfigFile runs the config file.
func (s *Setup) RunConfigFile() error {
	dotfileInputFile, err := fs.NewInputFile("//.gn", s.dotfileName)
	if err != nil {
		return fmt.Errorf("could not load dotfile: %w", err)
	}

	dotfileTokens, err := syntax.Tokenize(dotfileInputFile)
	if err != nil {
		return fmt.Errorf("tokenize failed: %w", err)
	}

	dotfileRoot, err := parse.Parse(dotfileTokens)
	if err != nil {
		return fmt.Errorf("parse failed: %w", err)
	}

	_, err = resolve.ExecuteNode(dotfileRoot, s.dotfileScope)
	if err != nil {
		return fmt.Errorf("execute failed: %w", err)
	}

	return nil
}

func (s *Setup) fillOtherConfig() error {
	// Secondary source path, read from the config file if present.
	// TODO: implement

	// Build file names.
	if value := s.dotfileScope.Value("build_file_extension", true); value != nil {
		stringValue, err := resolve.AsValue[*resolve.StringValue](value)
		if err != nil {
			return err
		}
		extension := stringValue.String()
		if strings.ContainsRune(extension, filepath.Separator) {
			return environment.BuildConfigError{
				Msg:  "Invalid build_file_extension",
				Help: fmt.Sprintf("Build file extension '%s' cannot contain a path separator", extension),
			}
		}
		s.loader.BuildFileExtension = "." + extension
	}

	// Ninja required version.
	// TODO: implement

	// Root build file.
	// TODO: implement i.e. read the "root" value or cmdline flag if provided
	// For now, just assume it's at //BUILD.gn
	rootDir, err := fs.MakeSourceDir("//")
	if err != nil {
		return fmt.Errorf("failed to init root build.gn")
	}
	rootTargetLabel := environment.Label{Dir: rootDir}

	// Set the root build file here in order to take into account the values of
	// "build_file_extension" and "root".
	s.rootBuildFile, err = s.loader.BuildFileForLabel(rootTargetLabel)
	if err != nil {
		return fmt.Errorf("failed to init root build.gn")
	}
	s.BuildSettings.RootTargetLabel = rootTargetLabel

	// Build config file.
	buildConfigValue := s.dotfileScope.Value("buildconfig", true)
	if buildConfigValue == nil {
		return environment.BuildConfigError{
			Msg:  "No build config file.",
			Help: fmt.Sprintf(`Your .gn file ("%s") didn't specify a "buildconfig" value.`, s.dotfileName),
		}
	}
	buildConfigFile, err := fs.MakeSourceFile(buildConfigValue.RawGNString())
	if err != nil {
		return err
	}
	s.BuildSettings.BuildConfigFile = buildConfigFile

	// Targets to check.
	// TODO: implement

	// Targets not to check.
	// TODO: implement

	// Fill exec_script_allowlist.
	// TODO: implement

	// Fill optional default args.
	// TODO: implement

	// No stamp files.
	// TODO: implement

	// Export compile commands.
	// TODO: implement

	// Append any additional export compile command patterns from the cmdline.
	// TODO: implement

	return nil
}

type pendingLoad struct {
	file   fs.SourceFile
	origin syntax.LocationRange
}

// Items returns a single-use iterator, running the build, and yielding successive items
// as the build progresses. Upon any failure, yields the error and halts the build.
func (s *Setup) Items() iter.Seq2[graph.Item, error] {
	return func(yield func(graph.Item, error) bool) {
		// TODO: run in parallel on errgroup.
		// make sure both Builder and Loader are thread-safe to convert to async.
		pending := []pendingLoad{{s.rootBuildFile, syntax.LocationRange{}}}
		for len(pending) > 0 {
			// TODO: support loads for other toolchains.
			items, err := s.loader.Load(pending[0].file, pending[0].origin, environment.Label{})
			if err != nil {
				yield(nil, err)
				return
			}
			pending = pending[1:]
			for _, item := range items {
				unresolvedDeps, allResolved, err := s.builder.RecordDefinedItem(item)
				if err != nil {
					yield(nil, err)
					return
				}
				for _, resolved := range allResolved {
					if !yield(resolved, nil) {
						return
					}
				}
				// Add all buildfiles from deps to queue.
				// NOTE: This is maybe inefficient since we don't check if multiple deps are
				// from the same buildfile. But the Loader will ignore seen buildfiles, so
				// it might be okay?
				for _, dep := range unresolvedDeps {
					depFile, err := s.loader.BuildFileForLabel(dep.Label)
					if err != nil {
						yield(nil, err)
						return
					}
					pending = append(pending, pendingLoad{depFile, dep.Origin.LocationRange()})
				}
			}
		}
	}
}
