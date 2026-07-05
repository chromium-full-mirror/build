// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/merkletree"
)

// TestFlush_TypeTransitions checks that Flush reconciles the on-disk entry with
// what hashfs records at a path, across every combination of file, symlink and
// directory (e.g. an incremental build where an output changed). For a type
// change the stale entry must be replaced -- otherwise os.WriteFile fails
// EISDIR, os.Symlink/os.Remove fails ENOTEMPTY, and os.MkdirAll fails
// ENOTDIR/EEXIST. Same-type cases exercise the in-place update paths: a file's
// contents and a symlink's target are refreshed, and a directory is kept (its
// stale members are left for dead-output cleanup, not cleared by flush).
func TestFlush_TypeTransitions(t *testing.T) {
	mtime := time.Unix(1000000000, 0)
	cmdhash := []byte("cmd")
	action := digest.Digest{Hash: "actionhash", SizeBytes: 10}
	const name = "gen/out"

	// putDisk materializes the given type at root/name on local disk, with a
	// non-empty directory so RemoveAll (not Remove) is exercised.
	putDisk := func(t *testing.T, root, typ string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		switch typ {
		case "file":
			if err := os.WriteFile(p, []byte("stale-file"), 0644); err != nil {
				t.Fatal(err)
			}
		case "symlink":
			if err := os.Symlink("stale-target", p); err != nil {
				t.Fatal(err)
			}
		case "dir":
			if err := os.MkdirAll(p, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p, "child"), []byte("stale-child"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// record registers the intended type in hashfs at root/name. A directory is
	// recorded as a not-local (build-without-the-bytes) output, which
	// materializes at flush via flushDir.
	record := func(t *testing.T, hfs *HashFS, root, typ string) {
		t.Helper()
		ctx := t.Context()
		var err error
		switch typ {
		case "file":
			err = hfs.WriteFile(ctx, root, path.Path(name), []byte("new-file"), false, mtime, cmdhash, nil)
		case "symlink":
			err = hfs.Symlink(ctx, root, "new-target", path.Path(name), mtime, cmdhash, nil)
		case "dir":
			err = hfs.Update(ctx, root, []UpdateEntry{{
				Name:    name,
				Entry:   &merkletree.Entry{Name: name},
				Mode:    0o755 | fs.ModeDir,
				ModTime: mtime,
				CmdHash: cmdhash,
				Action:  action,
			}})
		}
		if err != nil {
			t.Fatalf("record %s: %v", typ, err)
		}
	}

	types := []string{"file", "symlink", "dir"}
	for _, disk := range types {
		for _, want := range types {
			t.Run(disk+"_on_disk_to_"+want, func(t *testing.T) {
				if runtime.GOOS == "windows" && (disk == "symlink" || want == "symlink") {
					t.Skip("no symlink on windows")
				}
				ctx := t.Context()
				root := t.TempDir()
				root, err := filepath.EvalSymlinks(root)
				if err != nil {
					t.Fatal(err)
				}
				putDisk(t, root, disk)
				hfs, err := New(ctx, Option{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := hfs.Close(ctx); err != nil {
						t.Errorf("hfs.Close=%v", err)
					}
				})
				record(t, hfs, root, want)
				if err := hfs.Flush(ctx, root, []path.Path{path.Path(name)}); err != nil {
					t.Fatalf("Flush(%s on disk -> %s)=%v; want nil (stale %s must be replaced)", disk, want, err, disk)
				}
				p := filepath.Join(root, name)
				fi, err := os.Lstat(p)
				if err != nil {
					t.Fatalf("Lstat: %v", err)
				}
				switch want {
				case "file":
					if !fi.Mode().IsRegular() {
						t.Fatalf("on-disk mode=%v; want regular file", fi.Mode())
					}
					data, err := os.ReadFile(p)
					if err != nil {
						t.Fatal(err)
					}
					if got, want := string(data), "new-file"; got != want {
						t.Errorf("on-disk content=%q; want %q", got, want)
					}
				case "symlink":
					if fi.Mode()&os.ModeSymlink == 0 {
						t.Fatalf("on-disk mode=%v; want symlink", fi.Mode())
					}
					link, err := os.Readlink(p)
					if err != nil {
						t.Fatal(err)
					}
					if got, want := link, "new-target"; got != want {
						t.Errorf("on-disk symlink -> %q; want %q", got, want)
					}
				case "dir":
					if !fi.IsDir() {
						t.Fatalf("on-disk mode=%v; want directory", fi.Mode())
					}
					if disk == "dir" {
						// flushDir keeps an existing directory and does not clear
						// its contents (stale members are dead-output cleanup's
						// job, not flush's), so a pre-existing member is left
						// untouched.
						got, err := os.ReadFile(filepath.Join(p, "child"))
						if err != nil || string(got) != "stale-child" {
							t.Errorf("dir->dir: stale child = %q, err=%v; want %q left in place", got, err, "stale-child")
						}
					}
				}
			})
		}
	}
}
