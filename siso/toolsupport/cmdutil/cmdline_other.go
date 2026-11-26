// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build !windows

package cmdutil

// CheckCommandLine is a no-op on non-Windows platforms.
func CheckCommandLine(args []string) error {
	return nil
}
