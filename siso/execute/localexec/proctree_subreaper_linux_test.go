// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build linux

package localexec

import "golang.org/x/sys/unix"

// becomeSubreaper makes the test process a child subreaper, as the spawn
// helper makes itself one, so that drainGroup can reap orphans.
func becomeSubreaper() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}
