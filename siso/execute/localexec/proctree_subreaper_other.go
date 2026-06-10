// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build unix && !linux

package localexec

// becomeSubreaper is a no-op outside Linux: there is no PR_SET_CHILD_SUBREAPER
// equivalent. Orphaned descendants reparent to init/launchd, which reaps them,
// and kill(-pgid) returns ESRCH once only (skippable) zombies remain - so
// drainGroup terminates without needing to reap them ourselves.
func becomeSubreaper() error {
	return nil
}
