// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package gn

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	"go.chromium.org/build/gong/gn/build"
	"go.chromium.org/build/gong/gn/build/fs"
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
	buildSettings build.BuildSettings

	// FillArguments sets whether the build arguments should be filled during setup from the
	// command line/build argument file. This will be true by default. The use
	// case for setting it to false is when editing build arguments, we don't
	// want to rely on them being valid.
	FillArguments bool

	// Settings object for interpreting the .gn config file, and build arguments
	// from either the command line or build argument file.
	dotfileSettings *build.Settings
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
	setup.dotfileSettings = build.NewSettings(&setup.buildSettings)
	setup.dotfileScope = resolve.NewScopeFromExecContext(setup.dotfileSettings)
	return setup
}

// DoSetup configures the build for the current command line.
func (s *Setup) DoSetup(buildDir string, forceCreate bool, flags *CommonFlags) error {
	if flags.Time || flags.Tracelog != "" {
		fmt.Fprintf(os.Stderr, "tracing not yet implemented")
	}

	if err := s.FillSourceDir(flags); err != nil {
		return err
	}
	if err := s.RunConfigFile(); err != nil {
		fmt.Fprintf(os.Stderr, "don't know how to run config file yet, skipping for now: %v\n", err)
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

	// Check for unused variables in the .gn file.
	if err := s.dotfileScope.CheckForUnusedVars(); err != nil {
		return err
	}

	return fmt.Errorf("not implemented. setup: %v", s)
}

func (s *Setup) fillArguments(flags *CommonFlags) error {
	// TODO: implement properly
	if flags.Args != "" {
		return fmt.Errorf("don't know how to parse args from command line yet")
	}

	argsInputPath := path.Join(s.buildSettings.BuildDir, buildArgFileName)
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

	argScope := resolve.NewScopeFromExecContext(s.dotfileSettings)
	_, err = resolve.ExecuteNode(argsRoot, argScope)
	if err != nil {
		return fmt.Errorf("args execute failed: %w", err)
	}

	// TODO: do something with the resulting scope

	return nil
}

// FillSourceDir fills the root directory into the settings.
func (s *Setup) FillSourceDir(flags *CommonFlags) error {
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
	s.buildSettings.BuildDir = filepath.ToSlash(absBuildDir)
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
