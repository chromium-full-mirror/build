// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjabuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/toolsupport/ninjautil"
)

// DepsLogState is a state of a deps log entry.
type DepsLogState int

const (
	DepsLogUnknown DepsLogState = iota
	DepsLogStale
	DepsLogValid
)

func (s DepsLogState) String() string {
	switch s {
	case DepsLogUnknown:
		return "UNKNOWN"
	case DepsLogStale:
		return "STALE"
	case DepsLogValid:
		return "VALID"
	}
	return fmt.Sprintf("DepsLogState[%d]", int(s))
}

func (s DepsLogState) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// CheckDepsLogState checks deps log state by its output file.
// TODO(b/374196367): use digest for validity of output.
func CheckDepsLogState(ctx context.Context, hashFS *hashfs.HashFS, bpath *build.Path, target string, depsTime time.Time) (DepsLogState, string) {
	fi, err := hashFS.Stat(ctx, bpath.ExecRoot, bpath.MaybeFromWD(ctx, target))
	if err != nil {
		return DepsLogStale, fmt.Sprintf("not found deps output %q: %v", target, err)
	}
	if fi.ModTime().After(depsTime) {
		return DepsLogStale, fmt.Sprintf("output mtime %q: fs=%v depslog=%v", target, fi.ModTime(), depsTime)
	}
	return DepsLogValid, ""
}

// DepsLog is an in-memory representation of siso's depslog.
// It supports creating new depslog files and reading existing depslog files,
// as well as adding new records to open depslog files.
type DepsLog struct {
	fname string

	legacy *ninjautil.DepsLog

	// TODO(b/374196367): record digest as well as mtime for hash-based build.
}

// NewDepsLog reads or creates a new deps log.
// If there are read errors, returns a truncated deps log.
func NewDepsLog(ctx context.Context, fname string) (*DepsLog, error) {
	legacy, err := ninjautil.NewDepsLog(ctx, fname)
	if err != nil {
		return nil, err
	}
	if legacy.NeedsRecompact() {
		err = legacy.Recompact(ctx)
		if err != nil {
			clog.Warningf(ctx, "failed to recompact deps log: %v", err)
			return nil, err
		}
	}
	return &DepsLog{
		fname:  fname,
		legacy: legacy,
	}, nil
}

// Reset resets deps log, so recorded entries is available for RetrievePaths/IDs.
func (d *DepsLog) Reset() {
	if d.legacy != nil {
		d.legacy.Reset()
	}
}

// Close closes the deps log.
func (d *DepsLog) Close() error {
	if d.legacy != nil {
		return d.legacy.Close()
	}
	return nil
}

// RetrievePaths returns deps log for the output, converting from id to path.
// TODO(b/374196367): return digest of output.
func (d *DepsLog) RetrievePaths(ctx context.Context, output string) ([]string, time.Time, error) {
	if d.legacy != nil {
		return d.legacy.RetrievePaths(ctx, output)
	}
	return nil, time.Time{}, errors.New("no deps log")
}

// RetrieveIDs returns deps log for the output.
// TODO(b/374196367): return digest of output.
func (d *DepsLog) RetrieveIDs(ctx context.Context, out string) ([]int, time.Time, error) {
	if d.legacy != nil {
		return d.legacy.RetrieveIDs(ctx, out)
	}
	return nil, time.Time{}, errors.New("no deps log")
}

// NumPaths returns number of paths read at startup time.
func (d *DepsLog) NumPaths() int {
	if d.legacy != nil {
		return d.legacy.NumPaths()
	}
	return 0
}

// Path returns pathname for path id.
func (d *DepsLog) Path(id int) (string, error) {
	if d.legacy != nil {
		return d.legacy.Path(id)
	}
	return "", errors.New("no deps log")
}

// Record records deps log for the output. This will write to disk.
// Returns whether any deps were updated.
// TODO(b/374196367): record digest of output.
func (d *DepsLog) Record(ctx context.Context, output string, mtime time.Time, deps []string) (bool, error) {
	if d.legacy != nil {
		return d.legacy.Record(ctx, output, mtime, deps)
	}
	return false, nil
}

// RecordedTargets returns a list of targets that have deps log.
func (d *DepsLog) RecordedTargets() []string {
	if d.legacy != nil {
		return d.legacy.RecordedTargets()
	}
	return nil
}
