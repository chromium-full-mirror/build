// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build windows

package localexec

import (
	"context"
	"os/exec"

	"go.chromium.org/build/siso/o11y/clog"
)

// Windows has no unix process groups, so these are no-op/direct-child stubs;
// descendants of a cancelled action aren't tracked. The follow-up is a Job
// Object (JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE + wait for
// JOB_OBJECT_MSG_ACTIVE_PROCESS_ZERO, as Bazel does) to give the cancellation
// path cancelGroup/drainGroup parity; the completion path needs nothing - it
// waits for stdout/stderr EOF like Ninja, see runOnce.

func setProcGroup(c *exec.Cmd) {}

// cancelGroup kills the direct child only (Go can't signal a group on Windows).
func cancelGroup(ctx context.Context, c *exec.Cmd) error {
	err := c.Process.Kill()
	clog.Warningf(ctx, "send kill to pid=%d: %v", c.Process.Pid, err)
	// Return err verbatim: nil when we actually killed a live child, so os/exec
	// reports the cancellation (c.ctx.Err()); os.ErrProcessDone when the child had
	// already exited, so os/exec keeps the action's real exit status instead of
	// injecting context.Canceled over a successful exit (see watchCtx/Wait). This
	// matches the unix cancelGroup's ESRCH handling.
	return err
}

// drainGroup is a no-op stub here: without Job Objects there is nothing to
// sweep a finished action's leftovers with.
func drainGroup(ctx context.Context, pgid int) {}
