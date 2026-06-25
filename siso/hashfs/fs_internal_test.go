// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"go.chromium.org/build/siso/hashfs/osfs"
)

func TestDirectoryLookup_Symlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no symlink on windows")
		return
	}
	ctx := t.Context()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	setupFile := func(fname string) {
		t.Helper()
		fname = filepath.Join(dir, fname)
		err := os.MkdirAll(filepath.Dir(fname), 0755)
		if err != nil {
			t.Fatal(err)
		}
		err = os.WriteFile(fname, nil, 0644)
		if err != nil {
			t.Fatal(err)
		}
	}
	setupSymlink := func(fname, target string) {
		t.Helper()
		fname = filepath.Join(dir, fname)
		err := os.MkdirAll(filepath.Dir(fname), 0755)
		if err != nil {
			t.Fatal(err)
		}
		err = os.Symlink(target, fname)
		if err != nil {
			t.Fatal(err)
		}
	}

	fileName := "build/mac_files/xcode_binaries/Contents/Developer/Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk/somefile"
	setupFile(fileName)
	symlinkName := filepath.Join(filepath.Dir(filepath.Dir(fileName)), "MacOSX13.3.sdk")
	setupSymlink(symlinkName, "MacOSX.sdk")

	d := &directory{isRoot: true}
	osfs := osfs.New(ctx, "fs", osfs.Option{})

	fname := filepath.Join(dir, symlinkName)
	_, _, _, ok := d.lookup(ctx, fname)
	if ok {
		t.Fatalf("d.lookup(ctx, %q): %t; want false", fname, ok)
	}
	e := newLocalEntry()
	e.init(ctx, fname, nil, osfs)
	_, err = d.store(ctx, fname, e)
	if err != nil {
		t.Fatalf("d.store(ctx, %q) %v; want nil err", fname, err)
	}

	fname = filepath.Join(dir, fileName)
	_, _, _, ok = d.lookup(ctx, fname)
	if ok {
		t.Fatalf("d.lookup(ctx, %q): %t; want false", fname, ok)
	}
	e = newLocalEntry()
	e.init(ctx, fname, nil, osfs)
	_, err = d.store(ctx, fname, e)
	if err != nil {
		t.Fatalf("d.store(ctx, %q) %v; want nil err", fname, err)
	}

	t.Log(fname)
	_, _, _, ok = d.lookup(ctx, fname)
	if !ok {
		t.Fatalf("d.lookup(ctx, %q) %t; want true", fname, ok)
	}
	fname = filepath.Dir(fname)
	t.Log(fname)
	_, _, _, ok = d.lookup(ctx, fname)
	if !ok {
		t.Fatalf("d.lookup(ctx, %q) %t; want true", fname, ok)
	}

	fname = filepath.Join(dir, symlinkName)
	t.Log(fname)
	_, _, _, ok = d.lookup(ctx, fname)
	if !ok {
		t.Fatalf("d.lookup(ctx, %q) %t; want true", fname, ok)
	}
	fname = filepath.Join(fname, "somefile")
	t.Log(fname)
	_, _, _, ok = d.lookup(ctx, fname)
	if !ok {
		t.Fatalf("d.lookup(ctx, %q) %t; want true", fname, ok)
	}
}

// TestExpandFlushDirs_DedupesNestedDirs verifies expandFlushDirs does not emit a
// path twice for overlapping directory targets (a directory and a nested one
// inside it); a duplicate makes the Flush loop block on a drained e.lready channel.
func TestExpandFlushDirs_DedupesNestedDirs(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	hfs, err := New(ctx, Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hfs.Close(ctx)
	if err := hfs.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(root, "d", "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "d", "top.txt"), []byte("T"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "d", "sub", "a.txt"), []byte("A"), 0644); err != nil {
		t.Fatal(err)
	}
	// Populate the in-memory hashfs tree so d and d/sub are directories.
	if _, err := hfs.ReadDir(ctx, root, "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := hfs.ReadDir(ctx, root, "d/sub"); err != nil {
		t.Fatal(err)
	}

	got := hfs.expandFlushDirs(ctx, root, []string{"d/", "d/sub/"})
	counts := make(map[string]int)
	for _, p := range got {
		counts[p]++
	}
	for p, n := range counts {
		if n > 1 {
			t.Errorf("expandFlushDirs emitted %q %d times; want 1 (result=%v)", p, n, got)
		}
	}
	if counts["d/sub/a.txt"] == 0 {
		t.Errorf("expandFlushDirs dropped d/sub/a.txt: %v", got)
	}
}

func TestClearStaleFileForDirOutput(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	hfs, err := New(ctx, Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hfs.Close(ctx)
	if err := hfs.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}

	// "d" is a directory, "f" a regular file; stat them to populate hashfs.
	if err := os.MkdirAll(filepath.Join(root, "d", "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "d", "x"), []byte("X"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("F"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := hfs.Stat(ctx, root, "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := hfs.Stat(ctx, root, "f"); err != nil {
		t.Fatal(err)
	}

	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	}

	// A directory recorded under a directory-output target is left alone: it
	// may be a legitimate directory output, not a kind flip.
	if err := hfs.ClearStaleFileForDirOutput(ctx, root, "d"); err != nil {
		t.Fatalf("ClearStaleFileForDirOutput(d): %v", err)
	}
	if !exists("d") {
		t.Errorf("directory wrongly removed for a directory-output target")
	}

	// Missing entry: no-op, no error.
	if err := hfs.ClearStaleFileForDirOutput(ctx, root, "nonexistent"); err != nil {
		t.Errorf("ClearStaleFileForDirOutput(nonexistent): %v; want nil", err)
	}

	// A file recorded under a directory-output target is a genuine
	// file->directory flip: the stale file must be removed.
	if err := hfs.ClearStaleFileForDirOutput(ctx, root, "f"); err != nil {
		t.Fatalf("ClearStaleFileForDirOutput(f): %v", err)
	}
	if exists("f") {
		t.Errorf("stale file not removed when target flipped to a directory output")
	}

	// A stale file present on disk but with no in-memory hashfs entry
	// (state reset/version bump, or a file produced outside siso) must
	// still be cleared by consulting disk; otherwise ensureActionOutputDirs'
	// MkdirAll fails ENOTDIR.
	if err := os.WriteFile(filepath.Join(root, "g"), []byte("G"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := hfs.ClearStaleFileForDirOutput(ctx, root, "g"); err != nil {
		t.Fatalf("ClearStaleFileForDirOutput(g, foreign on-disk file): %v", err)
	}
	if exists("g") {
		t.Errorf("stale on-disk file with no hashfs entry not removed; MkdirAll would fail ENOTDIR")
	}
}

func TestExpandDirInputs_EmptyDir(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	hfs, err := New(ctx, Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hfs.Close(ctx)
	if err := hfs.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "full"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "full", "a.txt"), []byte("A"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := hfs.ReadDir(ctx, root, "empty"); err != nil {
		t.Fatal(err)
	}
	if _, err := hfs.ReadDir(ctx, root, "full"); err != nil {
		t.Fatal(err)
	}

	got := hfs.expandDirInputs(ctx, root, []string{"empty/", "full/"})
	has := func(p string) bool {
		return slices.Contains(got, p)
	}
	// An empty directory input must not vanish: keep the bare dir so the
	// (empty) directory is still represented in the input tree.
	if !has("empty") {
		t.Errorf("expandDirInputs dropped empty directory input: got %v; want it to contain %q", got, "empty")
	}
	// A non-empty directory input still expands to its files.
	if !has("full/a.txt") {
		t.Errorf("expandDirInputs did not expand non-empty dir: got %v", got)
	}
}
