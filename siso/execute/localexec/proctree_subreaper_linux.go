// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build linux

package localexec

import "golang.org/x/sys/unix"

// becomeSubreaper marks the current process as a child subreaper, so that any
// descendant of an action whose intermediate parent exits reparents to us
// instead of to init. drainGroup/cancelGroup can then reap such orphans via
// wait4(-pgid), instead of depending on init to do it (which never happens when
// siso runs as PID 1 in a container with no reaper). Idempotent; call once.
func becomeSubreaper() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}
