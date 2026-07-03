// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/digest"
	"go.chromium.org/build/siso/reapi/merkletree"
)

// TestFlush_LocalReadyDirInputNotFlushed verifies Flush leaves a local-ready
// directory alone. prepareLocalInputs flushes inputs too, so a source directory
// input reaches Flush; materializing it (flushDir) could Chtimes-reset it or,
// when the on-disk type changed, RemoveAll a source path as if it were a stale
// output. A recorded directory whose on-disk path was replaced by a file must
// be left in place, not treated as a stale output and removed.
func TestFlush_LocalReadyDirInputNotFlushed(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "srcdir")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	hfs, err := New(ctx, Option{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := hfs.Close(ctx); err != nil {
			t.Errorf("hfs.Close=%v", err)
		}
	})
	// Stat records srcdir as a local-ready directory input.
	if _, err := hfs.Stat(ctx, root, "srcdir"); err != nil {
		t.Fatalf("stat: %v", err)
	}

	// The on-disk source is replaced by a file after being recorded.
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("now-a-file"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := hfs.Flush(ctx, root, []path.Path{"srcdir"}); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got, err := os.ReadFile(src)
	if err != nil || string(got) != "now-a-file" {
		t.Errorf("source after flush = %q, err=%v; want %q left in place (local-ready input not flushed as a stale output)", got, err, "now-a-file")
	}
}

// TestFlush_PopulatedDirReplacesStaleNonDir covers a trailing-slash directory
// output whose path is still a stale file or symlink on disk from a previous
// build. Flush expands the directory into its members and flushes them
// concurrently; each member's flush MkdirAll's its parent. The parent directory
// must be materialized (stale entry removed, directory created) before any
// member flush runs, or a member races ahead and MkdirAll fails ENOTDIR (or
// follows the stale symlink). Many members make the race reliable.
func TestFlush_PopulatedDirReplacesStaleNonDir(t *testing.T) {
	mtime := time.Unix(1000000000, 0)
	cmdhash := []byte("cmd")
	action := digest.Digest{Hash: "actionhash", SizeBytes: 10}
	const dir = "gen/out"
	const nchild = 24

	for _, disk := range []string{"file", "symlink"} {
		t.Run(disk, func(t *testing.T) {
			if runtime.GOOS == "windows" && disk == "symlink" {
				t.Skip("no symlink on windows")
			}
			ctx := t.Context()
			root := t.TempDir()
			root, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(root, dir)
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				t.Fatal(err)
			}
			switch disk {
			case "file":
				if err := os.WriteFile(p, []byte("stale-file"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("stale-target", p); err != nil {
					t.Fatal(err)
				}
			}

			hfs, err := New(ctx, Option{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := hfs.Close(ctx); err != nil {
					t.Errorf("hfs.Close=%v", err)
				}
			})

			// Record the directory as a not-local (build-without-the-bytes)
			// output, plus many member files under it.
			if err := hfs.Update(ctx, root, []UpdateEntry{{
				Name:    dir,
				Entry:   &merkletree.Entry{Name: dir},
				Mode:    0o755 | fs.ModeDir,
				ModTime: mtime,
				CmdHash: cmdhash,
				Action:  action,
			}}); err != nil {
				t.Fatalf("update dir: %v", err)
			}
			for i := range nchild {
				child := fmt.Sprintf("%s/child%02d", dir, i)
				if err := hfs.WriteFile(ctx, root, path.Path(child), []byte("c"), false, mtime, cmdhash, nil); err != nil {
					t.Fatalf("writefile %s: %v", child, err)
				}
			}

			if err := hfs.Flush(ctx, root, []path.Path{path.Path(dir + "/")}); err != nil {
				t.Fatalf("Flush(populated dir over stale %s)=%v; want nil", disk, err)
			}

			fi, err := os.Lstat(p)
			if err != nil {
				t.Fatalf("Lstat(dir): %v", err)
			}
			if !fi.IsDir() {
				t.Fatalf("on-disk mode=%v; want directory", fi.Mode())
			}
			for i := range nchild {
				cp := filepath.Join(p, fmt.Sprintf("child%02d", i))
				b, err := os.ReadFile(cp)
				if err != nil || string(b) != "c" {
					t.Errorf("child%02d = %q, err=%v; want %q", i, b, err, "c")
				}
			}
		})
	}
}

// TestFlush_MemberListedBeforeDirTarget covers a caller that lists a directory
// output's members before the directory artifact itself -- e.g. a file output
// declared under a directory output, since builder.go appends file outputs
// ahead of dir+"/" outputs. expandFlushDirs must still order the parent
// directory ahead of its members so Flush materializes the directory (removing
// any stale file/symlink at its path) before a member's flush MkdirAll's the
// parent. Otherwise a member races ahead and MkdirAll fails ENOTDIR against the
// stale entry. Many members make the race reliable.
func TestFlush_MemberListedBeforeDirTarget(t *testing.T) {
	ctx := t.Context()
	mtime := time.Unix(1000000000, 0)
	cmdhash := []byte("cmd")
	action := digest.Digest{Hash: "actionhash", SizeBytes: 10}
	const dir = "gen/out"
	const nchild = 24

	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, dir)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	// A stale regular file sits where the directory output now belongs.
	if err := os.WriteFile(p, []byte("stale-file"), 0644); err != nil {
		t.Fatal(err)
	}

	hfs, err := New(ctx, Option{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := hfs.Close(ctx); err != nil {
			t.Errorf("hfs.Close=%v", err)
		}
	})
	if err := hfs.Update(ctx, root, []UpdateEntry{{
		Name:    dir,
		Entry:   &merkletree.Entry{Name: dir},
		Mode:    0o755 | fs.ModeDir,
		ModTime: mtime,
		CmdHash: cmdhash,
		Action:  action,
	}}); err != nil {
		t.Fatalf("update dir: %v", err)
	}
	// List every member first, then the directory artifact -- the order that
	// would defeat the synchronous directory flush without the parent-first
	// sort in expandFlushDirs.
	var files []string
	for i := range nchild {
		child := fmt.Sprintf("%s/child%02d", dir, i)
		if err := hfs.WriteFile(ctx, root, path.Path(child), []byte("c"), false, mtime, cmdhash, nil); err != nil {
			t.Fatalf("writefile %s: %v", child, err)
		}
		files = append(files, child)
	}
	files = append(files, dir+"/")

	if err := hfs.Flush(ctx, root, path.Paths(files)); err != nil {
		t.Fatalf("Flush(members before dir target)=%v; want nil", err)
	}

	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("Lstat(dir): %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("on-disk mode=%v; want directory", fi.Mode())
	}
	for i := range nchild {
		cp := filepath.Join(p, fmt.Sprintf("child%02d", i))
		b, err := os.ReadFile(cp)
		if err != nil || string(b) != "c" {
			t.Errorf("child%02d = %q, err=%v; want %q", i, b, err, "c")
		}
	}
}
