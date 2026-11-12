// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build !windows

package localexec

// concurrencyLimit returns n unchanged on non-Windows platforms.
func concurrencyLimit(n int) int {
	return n
}
