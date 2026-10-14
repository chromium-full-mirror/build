// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package localexec

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestDeleteSandbox_WithoutWorkDir checks that deleteSandbox removes a
// sandbox that has no overlayfs work/ directory.
func TestDeleteSandbox_WithoutWorkDir(t *testing.T) {
	// Create a sandbox with a file in it but no work/ directory.
	dir := filepath.Join(t.TempDir(), "sandbox")
	if err := os.MkdirAll(filepath.Join(dir, "root"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root", "out.txt"), []byte("out"), 0644); err != nil {
		t.Fatal(err)
	}

	// Delete it.
	deleteSandbox(dir)
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lstat(sandbox) after deleteSandbox: err = %v, want %v", err, fs.ErrNotExist)
	}
}

// TestDeleteSandbox_RestrictedDir checks that deleteSandbox removes a
// sandbox containing a non-empty directory that its owner lacks some
// permissions on.
func TestDeleteSandbox_RestrictedDir(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode fs.FileMode
	}{
		{name: "no_permissions", mode: 0000},
		{name: "not_readable", mode: 0300},
		{name: "not_writable", mode: 0500},
		{name: "not_searchable", mode: 0600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Create a sandbox with a file in a directory, then restrict the
			// directory.
			dir := filepath.Join(t.TempDir(), "sandbox")
			restricted := filepath.Join(dir, "root", "out")
			if err := os.MkdirAll(restricted, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(restricted, "out.txt"), []byte("out"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(restricted, tc.mode); err != nil {
				t.Fatal(err)
			}
			// Runs before t.TempDir's cleanup, which would fail on whatever
			// deleteSandbox leaves behind.
			t.Cleanup(func() { _ = os.Chmod(restricted, 0700) })

			// Delete it.
			deleteSandbox(dir)
			if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Lstat(sandbox) after deleteSandbox: err = %v, want %v", err, fs.ErrNotExist)
			}
		})
	}
}
