// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package nsjailutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRenameFromJail verifies renameFromJail captures an output out of the nsjail exec root, replacing a non-empty pre-created destination cleanly (a plain os.Rename of a dir onto a non-empty dir fails ENOTEMPTY).
func TestRenameFromJail(t *testing.T) {
	t.Run("file overwrites destination", func(t *testing.T) {
		dir := t.TempDir()
		jail := filepath.Join(dir, "jail")
		dst := filepath.Join(dir, "dst")
		if err := os.MkdirAll(jail, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jail, "out.txt"), []byte("NEW"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dst, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, "out.txt"), []byte("OLD"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := renameFromJail(filepath.Join(jail, "out.txt"), filepath.Join(dst, "out.txt")); err != nil {
			t.Fatalf("renameFromJail(file): %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dst, "out.txt"))
		if err != nil || string(got) != "NEW" {
			t.Fatalf("dst content = %q, %v; want NEW", got, err)
		}
	})

	t.Run("dir onto populated destination", func(t *testing.T) {
		dir := t.TempDir()
		jail := filepath.Join(dir, "jail", "gen")
		dst := filepath.Join(dir, "out", "gen")
		if err := os.MkdirAll(jail, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jail, "new.txt"), []byte("NEW"), 0644); err != nil {
			t.Fatal(err)
		}
		// Destination pre-created and populated by a prior build.
		if err := os.MkdirAll(dst, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, "stale.txt"), []byte("OLD"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := renameFromJail(jail, dst); err != nil {
			t.Fatalf("renameFromJail(dir onto populated dir): %v", err)
		}
		if _, err := os.Stat(filepath.Join(dst, "new.txt")); err != nil {
			t.Errorf("captured dir missing new.txt: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dst, "stale.txt")); !os.IsNotExist(err) {
			t.Errorf("stale file from previous build survived capture: err=%v", err)
		}
	})
}
