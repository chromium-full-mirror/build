// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execute

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"

	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
)

// TestCmdDigestInputGroups checks that Digest without a blob store gives the
// same digest with InputGroups as with the group paths in Inputs, whether
// the group subtree is used or not.
func TestCmdDigestInputGroups(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses symlinks")
	}
	ctx := t.Context()
	dir := t.TempDir()
	write := func(f string) {
		t.Helper()
		fname := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fname, []byte(f), 0644); err != nil {
			t.Fatal(err)
		}
	}
	symlink := func(target, f string) {
		t.Helper()
		fname := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, fname); err != nil {
			t.Fatal(err)
		}
	}
	tc := "prebuilts/clang/host/clang-r1"
	var toolchain []path.Path
	for i := range 40 {
		f := fmt.Sprintf("%s/lib/clang/include/sub%d/h%d.h", tc, i%5, i)
		write(f)
		toolchain = append(toolchain, path.Path(f))
	}
	write(tc + "/bin/clang")
	toolchain = append(toolchain, path.Path(tc+"/bin/clang"))
	symlink("clang", tc+"/bin/clang++")
	toolchain = append(toolchain, path.Path(tc+"/bin/clang++"))
	write(tc + "/lib/libc++.so.1")
	symlink("libc++.so.1", tc+"/lib/libc++.so")
	toolchain = append(toolchain, path.Path(tc+"/lib/libc++.so.1"), path.Path(tc+"/lib/libc++.so"))
	// a group whose symlink leaves its common dir.
	write("other/x/a.h")
	symlink("../../prebuilts/clang/host/clang-r1/bin/clang", "other/x/cc")
	escaping := []path.Path{"other/x/a.h", "other/x/cc"}

	// symlinked dir listed after files under it (phony DFS order).
	write("tc2/lib/clang/21/include/stddef.h")
	symlink("21", "tc2/lib/clang/current")
	write("tc2/bin/clang")
	dirOrder := []path.Path{"tc2/lib/clang/current/include/stddef.h", "tc2/lib/clang/current", "tc2/bin/clang"}
	// symlink that leaves the group dir and comes back.
	write("tc3/lib/a.h")
	write("tc3/bin/x")
	symlink("../../tc3/lib", "tc3/bin/cur")
	outAndBack := []path.Path{"tc3/bin/cur", "tc3/bin/cur/a.h", "tc3/lib/a.h", "tc3/bin/x"}

	// symlink to the group dir itself.
	write("tc4/bin/x")
	write("tc4/y")
	symlink("..", "tc4/bin/self")
	selfLink := []path.Path{"tc4/bin/self", "tc4/bin/self/y", "tc4/y", "tc4/bin/x"}

	write("src/a.cc")
	write("src/b.h")
	write(tc + "/extra.txt")
	symlink("a.cc", "src/link.cc")
	write("out/gen/g.h")

	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hashFS.Close(ctx)
	if err := hashFS.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}

	tcGroup := InputGroup{Key: "toolchain", Paths: toolchain}
	gc := NewInputGroupCache("")
	for _, tt := range []struct {
		name     string
		workDir  path.Path
		inputs   []path.Path
		groups   []InputGroup
		wantHits int64 // subtree used
	}{
		{
			name:     "source_and_group",
			inputs:   []path.Path{"src/a.cc", "src/b.h"},
			groups:   []InputGroup{tcGroup},
			wantHits: 1,
		},
		{
			name:     "own_input_in_group",
			inputs:   []path.Path{"src/a.cc", path.Path(tc + "/bin/clang++")},
			groups:   []InputGroup{tcGroup},
			wantHits: 1,
		},
		{
			name:     "own_input_under_group_dir",
			inputs:   []path.Path{"src/a.cc", path.Path(tc + "/extra.txt")},
			groups:   []InputGroup{tcGroup},
			wantHits: 0,
		},
		{
			name:     "own_dir_input_above_group",
			inputs:   []path.Path{"src/a.cc", "prebuilts/clang"},
			groups:   []InputGroup{tcGroup},
			wantHits: 1,
		},
		{
			// Entries expands the directory target to its files, and
			// extra.txt is not a group member.
			name:     "own_dir_target_above_group",
			inputs:   []path.Path{"src/a.cc", "prebuilts/clang/"},
			groups:   []InputGroup{tcGroup},
			wantHits: 0,
		},
		{
			name:     "own_symlink",
			inputs:   []path.Path{"src/link.cc"},
			groups:   []InputGroup{tcGroup},
			wantHits: 0,
		},
		{
			name:     "escaping_symlink_group",
			inputs:   []path.Path{"src/a.cc"},
			groups:   []InputGroup{{Key: "escaping", Paths: escaping}},
			wantHits: 0,
		},
		{
			name:     "workdir_in_group",
			workDir:  path.Path(tc + "/bin"),
			inputs:   []path.Path{"src/a.cc"},
			groups:   []InputGroup{tcGroup},
			wantHits: 0,
		},
		{
			name:     "two_groups",
			inputs:   []path.Path{"src/a.cc", "out/gen/g.h"},
			groups:   []InputGroup{tcGroup, {Key: "src", Paths: []path.Path{"src/b.h", "src/a.cc"}}},
			wantHits: 1,
		},
		{
			name:     "symlink_dir_after_files",
			inputs:   []path.Path{"src/a.cc"},
			groups:   []InputGroup{{Key: "dirOrder", Paths: dirOrder}},
			wantHits: 1,
		},
		{
			name:     "symlink_out_and_back",
			inputs:   []path.Path{"src/a.cc"},
			groups:   []InputGroup{{Key: "outAndBack", Paths: outAndBack}},
			wantHits: 0,
		},
		{
			name:     "symlink_to_group_root",
			inputs:   []path.Path{"src/a.cc"},
			groups:   []InputGroup{{Key: "selfLink", Paths: selfLink}},
			wantHits: 0,
		},
		{
			name:     "unclean_own_input_into_group",
			inputs:   []path.Path{"src/a.cc", path.Path("src/../" + tc + "/extra.txt")},
			groups:   []InputGroup{tcGroup},
			wantHits: 0,
		},
		{
			name:     "dir_target_group",
			inputs:   []path.Path{"src/a.cc"},
			groups:   []InputGroup{{Key: "dirTarget", Paths: []path.Path{"tc3/lib/", "tc3/bin/x"}}},
			wantHits: 0,
		},
		{
			name:     "overlapping_groups",
			inputs:   []path.Path{"src/a.cc"},
			groups:   []InputGroup{tcGroup, {Key: "tc-bin", Paths: []path.Path{path.Path(tc + "/bin/clang")}}},
			wantHits: 0,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workDir := tt.workDir
			if workDir == "" {
				workDir = "out"
			}
			c := &Cmd{
				ID:              tt.name,
				Args:            []string{"cc"},
				WorkspaceRoot:   dir,
				WorkDir:         workDir,
				Pure:            true,
				HashFS:          hashFS,
				Inputs:          tt.inputs,
				InputGroups:     tt.groups,
				InputGroupCache: gc,
			}
			st0 := gc.Stats()
			got, err := c.Digest(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			st1 := gc.Stats()
			if hits := st1.Spliced - st0.Spliced; hits != tt.wantHits {
				t.Errorf("subtree used %d times; want %d", hits, tt.wantHits)
			}
			if keys := st1.Spliced + st1.Fallbacks - st0.Spliced - st0.Fallbacks; keys != 1 {
				t.Errorf("spliced+fallbacks=%d; want 1", keys)
			}
			fc := &Cmd{
				ID:            tt.name,
				Args:          []string{"cc"},
				WorkspaceRoot: dir,
				WorkDir:       workDir,
				Pure:          true,
				HashFS:        hashFS,
				Inputs:        c.flattenInputGroups(),
			}
			want, err := fc.Digest(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("Digest with groups=%v; flat %v", got, want)
			}
		})
	}
}

// TestInputGroupsCanceled checks that a group examined with a canceled
// context is not cached, so a later step can still use it.
func TestInputGroupsCanceled(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	var paths []path.Path
	for i := range 10 {
		f := fmt.Sprintf("tc/d%d/f%d.h", i%3, i)
		fname := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fname, []byte(f), 0644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path.Path(f))
	}
	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hashFS.Close(ctx)
	if err := hashFS.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	c := &Cmd{WorkspaceRoot: dir, WorkDir: ".", HashFS: hashFS}
	g := InputGroup{Key: "tc", Paths: paths}
	gc := NewInputGroupCache("")
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if st := gc.subtree(cctx, c, g); st != nil {
		t.Errorf("canceled: subtree %v; want nil", st)
	}
	// a canceled and a live caller at the same time.
	var wg sync.WaitGroup
	var live *groupSubtree
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = gc.subtree(cctx, c, g)
	}()
	go func() {
		defer wg.Done()
		live = gc.subtree(ctx, c, g)
	}()
	wg.Wait()
	if live == nil || live.dir != "tc" {
		t.Fatalf("live: %v; want subtree at tc", live)
	}
	if st := gc.subtree(ctx, c, g); st != live {
		t.Errorf("later: %p; want cached %p", st, live)
	}
}

// TestInputGroupCachesIndependent checks that two caches (of two builds)
// don't share subtrees, exclude dirs or verification state.
func TestInputGroupCachesIndependent(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	var paths []path.Path
	for i := range 4 {
		f := fmt.Sprintf("tc/d%d/f%d.h", i%2, i)
		fname := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fname, []byte(f), 0644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path.Path(f))
	}
	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hashFS.Close(ctx)
	if err := hashFS.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	groups := []InputGroup{{Key: "tc", Paths: paths}}
	excluded := NewInputGroupCache("tc")
	gc := NewInputGroupCache("")
	for _, c := range []*InputGroupCache{excluded, gc} {
		cmd := &Cmd{Args: []string{"cc"}, WorkspaceRoot: dir, WorkDir: ".", Pure: true, HashFS: hashFS, InputGroups: groups, InputGroupCache: c}
		if _, err := cmd.Digest(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	if st := excluded.Stats(); st.Spliced != 0 || st.Fallbacks != 1 {
		t.Errorf("excluded: %+v; want 1 fallback", st)
	}
	if st := gc.Stats(); st.Spliced != 1 || st.Fallbacks != 0 {
		t.Errorf("gc: %+v; want 1 spliced", st)
	}
	if !excluded.SkipUnusable(groups) || gc.SkipUnusable(groups) {
		t.Errorf("SkipUnusable: excluded %t, gc %t; want true, false", excluded.SkipUnusable(groups), gc.SkipUnusable(groups))
	}
	excluded.ReportMismatch(ctx, "step", groups, "a", "b")
	if !excluded.Broken() || gc.Broken() {
		t.Errorf("Broken: excluded %t, gc %t; want true, false", excluded.Broken(), gc.Broken())
	}
}

func TestSymlinkStaysIn(t *testing.T) {
	for _, tc := range []struct {
		reldir, target string
		want           bool
	}{
		{"bin", "clang", true},
		{"bin", "../lib/x", true},
		{"lib/clang", "21", true},
		{"bin", "../../tc/lib", false},
		{".", "../x", false},
		{"bin", "/abs", false},
		{"bin", `..\x`, false},
		{"a/b", "../../c", true},
		{"a/b", "../../../c", false},
		{".", ".", false},
		{"bin", "..", false},
		{"a/b", "../..", false},
		{".", "clang", true},
	} {
		if got := symlinkStaysIn(tc.reldir, tc.target); got != tc.want {
			t.Errorf("symlinkStaysIn(%q, %q)=%t; want %t", tc.reldir, tc.target, got, tc.want)
		}
	}
}

func TestCommonDir(t *testing.T) {
	for _, tc := range []struct{ a, b, want string }{
		{"a/b/c", "a/b/c", "a/b/c"},
		{"a/b/c", "a/b/d", "a/b"},
		{"a/b", "a/b/c/d", "a/b"},
		{"a/bc", "a/b", "a"},
		{"a", "b", "."},
		{"x/y", "x", "x"},
	} {
		if got := CommonDir(tc.a, tc.b); got != tc.want {
			t.Errorf("CommonDir(%q, %q)=%q; want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

// BenchmarkDigestInputGroups compares the two-phase cache key computation
// for a compile-like step (a source file plus an 8000-file toolchain) with
// the toolchain as plain inputs, as an input group, and as an input group
// that can't be used as a subtree.
func BenchmarkDigestInputGroups(b *testing.B) {
	ctx := b.Context()
	dir := b.TempDir()
	tc := "prebuilts/clang/host/linux-x86/clang-r1"
	var toolchain []path.Path
	for i := range 8000 {
		f := fmt.Sprintf("%s/lib/clang/21/include/d%d/e%d/h%d.h", tc, i%40, i%7, i)
		fname := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(fname, []byte(f), 0644); err != nil {
			b.Fatal(err)
		}
		toolchain = append(toolchain, path.Path(f))
	}
	var srcs []path.Path
	for i := range 64 {
		f := fmt.Sprintf("frameworks/base/libs/hwui/f%d.cpp", i)
		fname := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(fname, []byte(f), 0644); err != nil {
			b.Fatal(err)
		}
		srcs = append(srcs, path.Path(f))
	}
	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		b.Fatal(err)
	}
	defer hashFS.Close(ctx)
	if err := hashFS.WaitReady(ctx); err != nil {
		b.Fatal(err)
	}
	// compute the file digests before the first mode.
	warm := &Cmd{ID: "warm", Args: []string{"clang++"}, WorkspaceRoot: dir, WorkDir: ".", Pure: true, HashFS: hashFS, Inputs: append(slices.Clone(srcs), toolchain...)}
	if _, err := warm.Digest(ctx, nil); err != nil {
		b.Fatal(err)
	}
	group := InputGroup{Key: "tc", Paths: toolchain}
	gc := NewInputGroupCache("")
	// a cache in which the group is unusable.
	excluded := NewInputGroupCache(path.Path(tc))
	for _, mode := range []string{"flat", "groups", "fallback"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				c := &Cmd{
					ID:            "cc",
					Args:          []string{"clang++", "-c", string(srcs[i%len(srcs)])},
					WorkspaceRoot: dir,
					WorkDir:       ".",
					Pure:          true,
					HashFS:        hashFS,
				}
				switch mode {
				case "flat":
					c.Inputs = append([]path.Path{srcs[i%len(srcs)]}, toolchain...)
				case "groups":
					c.Inputs = []path.Path{srcs[i%len(srcs)]}
					c.InputGroups = []InputGroup{group}
					c.InputGroupCache = gc
				case "fallback":
					c.Inputs = []path.Path{srcs[i%len(srcs)]}
					c.InputGroups = []InputGroup{group}
					c.InputGroupCache = excluded
				}
				if _, err := c.Digest(ctx, nil); err != nil {
					b.Fatal(err)
				}
				i++
			}
		})
	}
}
