// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninja

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	log "github.com/golang/glog"

	"go.chromium.org/build/siso/o11y/clog"
)

func (c *Command) initLogDir(ctx context.Context) error {
	if !filepath.IsAbs(c.logDir) {
		logDir, err := filepath.Abs(c.logDir)
		if err != nil {
			return fmt.Errorf("abspath for log dir: %w", err)
		}
		c.logDir = logDir
	}
	err := os.MkdirAll(c.logDir, 0755)
	if err != nil {
		return err
	}
	return c.logSymlink(ctx)
}

// glogFilename returns filename of glog logfile. i.e. siso.INFO.
func (c *Command) glogFilename() string {
	logFilename := "siso.INFO"
	if runtime.GOOS == "windows" {
		logFilename = "siso.exe.INFO"
	}
	return filepath.Join(c.logDir, logFilename)
}

func rotateFiles(ctx context.Context, fname string) {
	ext := filepath.Ext(fname)
	fnameBase := strings.TrimSuffix(fname, ext)

	oldestFilename := fmt.Sprintf("%s.9%s", fnameBase, ext)
	fi, err := os.Lstat(oldestFilename)
	if err == nil && fi.Mode().Type() == fs.ModeSymlink {
		target, err := os.Readlink(oldestFilename)
		if err == nil {
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(oldestFilename), target)
			}
			err = os.Remove(target)
			clog.Infof(ctx, "remove oldest log %s: %v", target, err)
		}
	}
	// oldestFilename itself will be replaced with <base>.8<ext>.
	for i := 8; i >= 0; i-- {
		err := os.Rename(
			fmt.Sprintf("%s.%d%s", fnameBase, i, ext),
			fmt.Sprintf("%s.%d%s", fnameBase, i+1, ext))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			clog.Warningf(ctx, "rotate %s %d->%d failed: %v", fname, i, i+1, err)
		}
	}
	err = os.Rename(fname, fmt.Sprintf("%s.0%s", fnameBase, ext))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		clog.Warningf(ctx, "rotate %s ->0 failed: %v", fname, err)
	}
}

func (c *Command) logSymlink(ctx context.Context) error {
	logFilename := c.glogFilename()
	rotateFiles(ctx, logFilename)
	logfiles, err := log.Names("INFO")
	if err != nil {
		return fmt.Errorf("failed to get glog INFO level log files: %w", err)
	}
	if len(logfiles) == 0 {
		return fmt.Errorf("no glog INFO level log files")
	}
	err = os.Symlink(logfiles[0], logFilename)
	if err != nil {
		clog.Warningf(ctx, "failed to create %s: %v", logFilename, err)
		// On Windows, it failed to create symlink.
		// just same filename in *.redirected file.
		err = os.WriteFile(logFilename+".redirected", []byte(logfiles[0]), 0644)
		if err != nil {
			clog.Warningf(ctx, "failed to write %s.redirected: %v", logFilename, err)
		}
		c.sisoInfoLog = logfiles[0]
		return nil
	}
	clog.Infof(ctx, "logfile: %q", logfiles)
	c.sisoInfoLog = filepath.Base(logFilename)
	return nil
}

// logFilename returns siso's log filename relative to startDir, or absolute path.
func (c *Command) logFilename(fname, startDir string) string {
	if fname == "" {
		return ""
	}
	if !filepath.IsAbs(fname) {
		fname = filepath.Join(c.logDir, fname)
	}
	if startDir == "" {
		return fname
	}
	rel, err := filepath.Rel(startDir, fname)
	if err != nil || !filepath.IsLocal(rel) {
		return fname
	}
	return "." + string(os.PathSeparator) + rel
}

func (c *Command) logWriter(ctx context.Context, fname string) (io.Writer, func(errp *error), error) {
	fname = c.logFilename(fname, "")
	if fname == "" {
		return nil, func(*error) {}, nil
	}
	rotateFiles(ctx, fname)
	f, err := os.Create(fname)
	if err != nil {
		return nil, func(*error) {}, err
	}
	return f, func(errp *error) {
		clog.Infof(ctx, "close %s", fname)
		cerr := f.Close()
		if *errp == nil {
			*errp = cerr
		}
	}, nil
}

func (c *Command) setupCrashOutput(ctx context.Context) (func(), error) {
	fname := c.logFilename("siso_crash", "")
	rotateFiles(ctx, fname)
	crashFile, err := os.Create(fname)
	if err != nil {
		return nil, err
	}
	err = debug.SetCrashOutput(crashFile, debug.CrashOptions{})
	if err != nil {
		return nil, err
	}
	return func() { debug.SetCrashOutput(nil, debug.CrashOptions{}) }, crashFile.Close()
}
