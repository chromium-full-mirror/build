// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build windows

package localexec

// maxWindowsConcurrency caps concurrent local process spawns on Windows.
// Spawning >64 processes at once fails with "Not enough memory resources are
// available to process this command" (b/278658064, b/466257630) even with
// ample RAM.
const maxWindowsConcurrency = 64

// concurrencyLimit returns n capped at maxWindowsConcurrency on Windows.
func concurrencyLimit(n int) int {
	return min(n, maxWindowsConcurrency)
}
