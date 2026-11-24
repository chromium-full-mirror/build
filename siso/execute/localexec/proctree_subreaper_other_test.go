// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build unix && !linux

package localexec

// becomeSubreaper is a no-op outside Linux, which has no
// PR_SET_CHILD_SUBREAPER.
func becomeSubreaper() error {
	return nil
}
