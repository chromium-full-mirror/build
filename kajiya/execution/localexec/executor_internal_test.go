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
