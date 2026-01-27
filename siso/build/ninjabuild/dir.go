// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjabuild

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"go.chromium.org/build/siso/o11y/clog"
)

// DirFlag contains ninja directory flags.
type DirFlag struct {
	Dir           string
	ConfigRepoDir string
}

// RegisterFlags registers dir flags in fs.
func (f *DirFlag) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&f.Dir, "C", ".", "ninja running directory (chdir before run)")
	fs.StringVar(&f.ConfigRepoDir, "config_repo_dir", "build/config/siso", "config repo directory (relative to exec root)")
}

// InitDir prepares the environment for a build by resolving the exec root.
//
// It resolves the current working directory (evaluating symlinks),
// changes the working directory to f.Dir, and
// searches upward for f.ConfigRepoDir.
//
// It returns:
// - startDir: The absolute, symlink-free original directory.
// - execRoot: The absolute path to the execution root.
// - dir: The relative path from execRoot to the new working directory.
//
// current working directory becomes execRoot/dir, where
// execRoot/f.ConfigRepoDir exists.
func InitDir(ctx context.Context, f DirFlag) (startDir, execRoot, dir string, _ error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", "", "", err
	}
	wd, err = filepath.EvalSymlinks(wd)
	if err != nil {
		return "", "", "", err
	}
	clog.Infof(ctx, "wd: %s", wd)
	startDir = wd
	execRoot = startDir
	err = os.Chdir(f.Dir)
	if err != nil {
		return "", "", "", err
	}
	clog.Infof(ctx, "change dir to %s", f.Dir)
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", "", err
	}
	realCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		clog.Warningf(ctx, "failed to eval symlinks %q: %v", cwd, err)
	} else if cwd != realCWD {
		clog.Infof(ctx, "cwd %s -> %s", cwd, realCWD)
		cwd = realCWD
	}
	if !filepath.IsAbs(f.ConfigRepoDir) {
		execRoot, err = detectExecRoot(execRoot, f.ConfigRepoDir)
		if err != nil {
			return "", "", "", err
		}
	}
	rdir, err := filepath.Rel(execRoot, cwd)
	if err != nil {
		return "", "", "", err
	}
	if !filepath.IsLocal(rdir) {
		return "", "", "", fmt.Errorf("dir %q is out of exec root %q", cwd, execRoot)
	}
	return startDir, execRoot, rdir, nil
}

// detectExecRoot detects exec root from path given marker.
func detectExecRoot(execRoot, marker string) (string, error) {
	for {
		_, err := os.Stat(filepath.Join(execRoot, marker))
		if err == nil {
			return execRoot, nil
		}
		dir := filepath.Dir(execRoot)
		if dir == execRoot {
			// reached to root dir
			return "", fmt.Errorf("can not detect exec_root: %s not found", marker)
		}
		execRoot = dir
	}
}
