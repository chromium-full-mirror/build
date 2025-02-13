// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package gn

import (
	"fmt"
	"os"
	"path/filepath"

	"infra/build/gong/gn/build"
	"infra/build/gong/gn/fs"
	"infra/build/gong/gn/parse"
	"infra/build/gong/gn/resolve"
	"infra/build/gong/gn/syntax"
)

const gnFile = ".gn"

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

	// These settings are used to interpret the command line and dot file.
	dotfileSettings *build.Settings

	// State for invoking the dotfile.
	dotfileName string
}

// NewSetup creates a new Setup helper.
func NewSetup() *Setup {
	setup := &Setup{}
	setup.dotfileSettings = build.NewSettings(&setup.buildSettings)
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
		return err
	}

	return fmt.Errorf("not implemented. setup: %v", s)
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

	_, err = resolve.ExecuteNode(dotfileRoot)
	if err != nil {
		return fmt.Errorf("execute failed: %w", err)
	}

	return nil
}
