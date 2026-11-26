// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package cmdutil

import (
	"fmt"
	"unicode/utf16"
)

// maxCommandLineLength is the Windows CreateProcess lpCommandLine limit
// in UTF-16 code units, including the terminating null character.
// https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-createprocessw
const maxCommandLineLength = 32767

func checkCommandLineLength(cmdline string) error {
	n := 1 // +1 for null terminator.
	for _, r := range cmdline {
		n += utf16.RuneLen(r)
	}
	if n > maxCommandLineLength {
		return fmt.Errorf("command line is too long %d > %d", n, maxCommandLineLength)
	}
	return nil
}
