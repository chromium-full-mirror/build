// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build unix

package localexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"go.chromium.org/build/siso/o11y/clog"
)

// Process-tree management for non-console local actions, modeled on Bazel's
// process-wrapper: each runs as its own process group so cancel and post-exit
// reap reach every descendant, not just the direct child. A setsid() escapee
// still leaves the group (PID namespaces would be needed; possible follow-up).
//
// The spawn helper marks itself as a child subreaper (see becomeSubreaper,
// called once at spawn-helper startup), so a descendant orphaned mid-run - its
// intermediate parent exited - reparents to us instead of to init. That lets us
// reap it with wait4(-pgid): a SIGKILLed group member that nobody else reaps
// would otherwise linger as a zombie and keep kill(-pgid) succeeding forever
// (e.g. siso running as PID 1 in a container with no init reaper). Reaping it
// ourselves makes kill(-pgid) return ESRCH, so no /proc scan is needed to tell
// a live member from an unreaped corpse.
const (
	// How long to wait after initial SIGTERM before SIGKILL'ing the group
	killDelay = 1 * time.Second

	// Poll interval while waiting for process group to become empty.
	killInterval = 100 * time.Microsecond

	// Backstop for drainGroup: a legitimate drain reaps members in microseconds
	// and completes well under this, so exceeding it means a group member can't be
	// cleared (a setsid escapee's unreapable zombie, or a D-state child) and we
	// give up rather than hang the build.
	drainTimeout = 1 * time.Second
)

// setProcGroup makes the action its own process group leader (pgid == pid),
// applied by the runtime between fork and exec.
func setProcGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// cancelGroup is exec.Cmd.Cancel for setProcGroup actions: SIGTERM the group for
// cleanup, then SIGKILL it if it outlives killDelay. Wait can't return until
// Cancel does, so we poll the group itself (kill 0 probes, failing with ESRCH
// once empty), not the Cmd.
func cancelGroup(ctx context.Context, c *exec.Cmd) error {
	// Gracefully ask the processes to terminate.
	pgid := c.Process.Pid
	err := syscall.Kill(-pgid, syscall.SIGTERM)
	clog.Warningf(ctx, "send SIGTERM to pgid=%d: %v", pgid, err)
	if err == syscall.ESRCH {
		// The whole group is already gone: the action finished on its own and
		// cancellation merely lost the race to os/exec reaping the leader. Report
		// os.ErrProcessDone so os/exec keeps the action's real exit status instead
		// of injecting context.Canceled over a successful exit (see watchCtx/Wait).
		// (A leader that has exited but isn't yet reaped is still a zombie in the
		// group, so SIGTERM succeeds and we miss that sub-microsecond window;
		// distinguishing it would need a /proc state scan, not worth it.)
		return os.ErrProcessDone
	}
	if err != nil {
		// Unexpected (e.g. EPERM): surface it rather than masking it as a clean
		// cancellation.
		return err
	}

	// Give the group until killDelay to exit on its own after the SIGTERM...
	deadline := time.Now().Add(killDelay)
	for {
		// Reap descendants the subreaper reparented to us, so a multi-process
		// action's grandchildren (orphaned the instant their parent gets the
		// same SIGTERM) don't linger as our zombies and keep kill(-pgid, 0)
		// succeeding for the full killDelay. Only once os/exec has reaped the
		// group leader (pid == pgid, kill -> ESRCH) is wait4(-pgid) guaranteed
		// not to steal the main child's exit status out from under Cmd.Wait.
		if syscall.Kill(pgid, 0) == syscall.ESRCH {
			reapReparented(pgid)
		}
		if syscall.Kill(-pgid, 0) != nil {
			// Any error ends the poll (normally ESRCH: graceful exit complete).
			// Unlike drainGroup we don't single out ESRCH here: this is the cancel
			// path, the final SIGKILL below and the deferred drainGroup are the
			// authoritative reaper, and a non-ESRCH error (e.g. EPERM) just ends
			// the grace poll early - drainGroup still handles the survivor.
			break
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(killInterval)
	}

	// ...then SIGKILL whatever's left (no-op if the group already drained). The
	// deferred drainGroup after Cmd.Wait does the authoritative reap.
	syscall.Kill(-pgid, syscall.SIGKILL)

	return nil
}

// drainGroup SIGKILLs the process group and reaps every member until none remain
// (kill(-pgid) reaches ESRCH) or it gives up at drainTimeout with a member it
// couldn't clear. It is called synchronously after Cmd.Wait has reaped the group
// leader (the action's direct child), so wait4(-pgid) here only ever reaps the
// descendants the subreaper reparented to us, never the leader.
func drainGroup(ctx context.Context, pgid int) {
	deadline := time.Now().Add(drainTimeout)
	for {
		// Reap reparented descendants so their zombies stop keeping kill(-pgid)
		// alive. With PR_SET_CHILD_SUBREAPER they are our children; without it
		// (non-Linux) they belong to init/launchd and wait4 returns ECHILD,
		// leaving them to be reaped there - harmless, because on those systems
		// kill(-pgid) already returns ESRCH once only zombies remain.
		reapReparented(pgid)
		err := syscall.Kill(-pgid, syscall.SIGKILL)
		if groupGone(err) {
			return // ESRCH: group empty.
		}
		// err == nil: members remain (and were signalled). Any other error - e.g.
		// EPERM for a descendant that changed its real UID - means a member we
		// can't clear is still there; it is NOT drained, so keep trying until the
		// deadline rather than returning as if the group were empty.
		if !time.Now().Before(deadline) {
			// A group member is stuck and we can't clear it: a setsid escapee that
			// parents an unreapable zombie still in the group (kill(-pgid) keeps
			// succeeding but wait4(-pgid) can't reap a non-child), a descendant we
			// can't signal (EPERM), or a child wedged in uninterruptible (D-state)
			// sleep. These need PID namespaces to truly contain; give up rather
			// than hang the build (and so the helper still replies for the action).
			clog.Warningf(ctx, "drainGroup pgid=%d: gave up after %s (last kill: %v); likely a setsid escapee, a uid-changed, or a D-state descendant left an uncollectable group member", pgid, drainTimeout, err)
			return
		}
		time.Sleep(killInterval)
	}
}

// groupGone reports whether a kill(-pgid) error means the process group is empty.
// Only ESRCH does; any other error (in particular EPERM for a descendant that
// changed its real UID, which we then can't signal) means a member is still there,
// so the group is NOT drained.
func groupGone(killErr error) bool {
	return errors.Is(killErr, syscall.ESRCH)
}

// reapReparented reaps any already-exited members of process group pgid that the
// subreaper has reparented to us, so they don't linger as zombies. Callers must
// ensure the group leader (pid == pgid) is already reaped by os/exec, so this
// never steals the action's main child. A no-op where wait4 finds no such
// children (ECHILD) or none have exited yet (WNOHANG -> 0).
func reapReparented(pgid int) {
	for {
		wpid, err := unix.Wait4(-pgid, nil, unix.WNOHANG, nil)
		if wpid <= 0 || err != nil {
			return
		}
	}
}
