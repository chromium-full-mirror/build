// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build windows

package cmdutil

import "golang.org/x/sys/windows"

// CheckCommandLine checks that args formatted as a Windows CreateProcess
// command line does not exceed the 32,767 UTF-16 code unit limit (including
// the terminating null character).
func CheckCommandLine(args []string) error {
	return checkCommandLineLength(windows.ComposeCommandLine(args))
}
