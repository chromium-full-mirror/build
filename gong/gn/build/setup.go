// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package build builds a graph of GN targets based on an invocation.
package build

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"go.chromium.org/build/gong/gn"
	"go.chromium.org/build/gong/gn/build/analysis"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/runtime"
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
	buildSettings    runtime.BuildSettings
	loader           analysis.Loader
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
	setup.loader = analysis.MakeLoader(&setup.buildSettings, &setup.inputFileManager)
	setup.dotfileSettings = analysis.NewSettings(&setup.buildSettings)
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

	argsInputPath := path.Join(s.buildSettings.BuildDir.Path(), buildArgFileName)
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
			s.buildSettings.DotfileName = s.dotfileName
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
	s.buildSettings.SetRootPath(rootRealpath)

	return nil
}

func (s *Setup) fillBuildDir(buildDir string) error {
	// TODO: implement properly
	absBuildDir, err := filepath.Abs(buildDir)
	if err != nil {
		return err
	}
	s.buildSettings.BuildDir, err = fs.MakeSourceDir(absBuildDir)
	if err != nil {
		return err
	}
	return nil
}

func (s *Setup) fillPythonPath(flags *gn.CommonFlags) error {
	// TODO(b/388723392): Need parity with C++ GN's Windows handling
	// https://source.chromium.org/gn/gn/+/main:src/gn/setup.cc;l=791-825;drc=81dab9f25cb2381400c237fdea7030d5068f9a73
	// Maybe this can be resolved using exec.LookPath?
	// https://pkg.go.dev/os/exec?GOOS=windows#LookPath
	if goruntime.GOOS == "windows" {
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
		s.buildSettings.PythonPath = flags.ScriptExecutable
		return nil
	}

	// Use `script_executable` from the dotfile if available.
	if value := s.dotfileScope.Value("script_executable", true); value != nil {
		stringValue, err := resolve.AsValue[*resolve.StringValue](value)
		if err != nil {
			return err
		}
		s.buildSettings.PythonPath = stringValue.String()
		return nil
	}

	// Fallback to Python from PATH.
	// (Yes, this is a line of code referencing "python" in 2025 because that's
	// what C++ GN does, and you're probably going to want to override that
	// manually with `script_executable = "python3"` in your .gn file like these:
	// https://source.chromium.org/search?q=script_executable)
	s.buildSettings.PythonPath = "python"
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
			return runtime.BuildError(
				"Invalid build_file_extension",
				fmt.Sprintf("Build file extension '%s' cannot contain a path separator", extension))
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
	rootTargetLabel := runtime.Label{Dir: rootDir}

	// Set the root build file here in order to take into account the values of
	// "build_file_extension" and "root".
	s.rootBuildFile, err = s.loader.BuildFileForLabel(rootTargetLabel)
	if err != nil {
		return fmt.Errorf("failed to init root build.gn")
	}
	s.buildSettings.RootTargetLabel = rootTargetLabel

	// Build config file.
	buildConfigValue := s.dotfileScope.Value("buildconfig", true)
	if buildConfigValue == nil {
		return runtime.BuildError(
			"No build config file.",
			fmt.Sprintf(`Your .gn file ("%s") didn't specify a "buildconfig" value.`, s.dotfileName))
	}
	buildConfigFile, err := fs.MakeSourceFile(buildConfigValue.RawGNString())
	if err != nil {
		return err
	}
	s.buildSettings.BuildConfigFile = buildConfigFile

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

// Run runs the load, returning nil on success. On failure, returns the error.
func (s *Setup) Run() error {
	err := s.loader.Load(s.rootBuildFile, syntax.LocationRange{}, runtime.Label{})
	if err != nil {
		return err
	}

	// TODO: watch load/execute tasks, verify results once done.

	return nil
}
