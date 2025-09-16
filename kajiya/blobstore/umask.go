// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build unix

package blobstore

import "syscall"

// ResetUmask resets the process's umask to 022, which is a reasonable default.
// By setting umask to a known value, we know which permissions directories and
// files that we create will have, so we can reduce the number of calls to
// os.Chmod to only those cases where we actually need to change permissions.
// This improves performance for actions with many input files, by avoiding
// unnecessary syscalls.
func ResetUmask() {
	syscall.Umask(0o022)
}
