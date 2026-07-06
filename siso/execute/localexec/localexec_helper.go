// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build unix

package localexec

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/execute"
	epb "go.chromium.org/build/siso/execute/proto"
	"go.chromium.org/build/siso/execute/spawnhelper"
	"go.chromium.org/build/siso/o11y/clog"
)

// helper holds the running spawn helper once StartHelper launches it. runViaHelper
// uses it when set; when unset - under `go test`, or a subcommand that doesn't drive
// a build - actions run in-process. atomic so build workers read it without locking.
// Tests Store a helper directly.
var helper atomic.Pointer[spawnhelper.Client]

// StartHelper launches the spawn helper, so the large-heap siso never fork()s. Call
// it once, early, while siso's heap is still small. logFile names the file the helper
// writes its diagnostics to; the caller rotates any previous one and the helper
// creates it fresh.
func StartHelper(ctx context.Context, helperCommand, logFile string) error {
	args := []string{helperCommand}
	if helperCommand == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("spawn helper: %w", err)
		}
		args = []string{exe, "spawn-helper"}
	}
	c, err := spawnhelper.Launch(args, logFile)
	if err != nil {
		return fmt.Errorf("spawn helper: %w", err)
	}
	helper.Store(c)
	return nil
}

// StopHelper closes the control socket so the helper drains and exits, then waits
// for it. The helper reaps every local action, so its rusage cutime holds all of
// their CPU time. Waiting here is what rolls that up into siso's own rusage, so
// `time siso` and the build-time graph account for the local build's CPU. No-op
// when no helper was started (tests, non-build subcommands).
func StopHelper(ctx context.Context) error {
	c := helper.Swap(nil)
	if c == nil {
		return nil
	}
	if err := c.Close(); err != nil {
		clog.Warningf(ctx, "spawn helper close: %v", err)
	}
	return c.Wait()
}

// runViaHelper runs cmd through the spawn helper. When no helper was started it
// runs in-process; in a real build StartHelper ran first (and aborted the build on
// failure), so siso never silently fork()s its large heap here.
func runViaHelper(ctx context.Context, cmd *execute.Cmd) (*rpb.ActionResult, error) {
	c := helper.Load()
	if c == nil {
		return run(ctx, cmd)
	}

	var oomScoreAdj *int32
	if cmd.OOMScoreAdj != nil {
		oomScoreAdj = new(int32(*cmd.OOMScoreAdj))
	}
	req := &epb.SpawnRequest{
		Id:            cmd.ID,
		Args:          cmd.Args,
		Env:           cmd.Env,
		WorkspaceRoot: cmd.WorkspaceRoot,
		WorkDir:       string(cmd.WorkDir),
		OomScoreAdj:   oomScoreAdj,
	}
	res, err := c.Run(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("spawn helper: %w", err)
	}
	ar := &rpb.ActionResult{}
	if err := res.ActionResult.UnmarshalTo(ar); err != nil {
		return nil, fmt.Errorf("unmarshal action result: %w", err)
	}
	// The helper buffered the child's output and returned it inline; copy it into
	// cmd's buffers so callers reading cmd.Stdout()/Stderr() see it (mirrors the
	// remote-exec path in remoteexec.go and the cache path in cache.go).
	if len(ar.StdoutRaw) > 0 {
		cmd.StdoutWriter().Write(ar.StdoutRaw)
	}
	if len(ar.StderrRaw) > 0 {
		cmd.StderrWriter().Write(ar.StderrRaw)
	}
	return ar, nil
}
