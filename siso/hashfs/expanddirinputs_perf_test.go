// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/merkletree"
)

// mkTreeForDigest materializes a synthetic generated-output tree under root/gen
// with nFiles regular files across a few subdirectories.
func mkTreeForDigest(t testing.TB, root string, nFiles int) {
	t.Helper()
	const dir = "gen"
	const subdirs = 8
	for i := range nFiles {
		rel := filepath.Join(dir, fmt.Sprintf("sub%d", i%subdirs), fmt.Sprintf("f%d.dat", i))
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, fmt.Appendf(nil, "content-%d-payload", i), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func newDigestHashFS(t testing.TB) (*HashFS, string) {
	t.Helper()
	ctx := t.Context()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hfs, err := New(ctx, Option{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hfs.Close(context.WithoutCancel(ctx)) })
	if err := hfs.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	return hfs, root
}

// TestExpandDirInputs_IncludesEmptySubdir is a regression test: a directory
// input containing an empty subdirectory must expand to include that subdir, so
// a remote action's input tree matches local disk. The output side preserves
// empty dirs (dirOutputTree); the input side must too.
func TestExpandDirInputs_IncludesEmptySubdir(t *testing.T) {
	hfs, root := newDigestHashFS(t)
	if err := os.MkdirAll(filepath.Join(root, "d", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "d", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := hfs.expandDirInputs(t.Context(), root, []path.Path{"d/"})
	if !slices.Contains(got, "d/empty") {
		t.Errorf("expandDirInputs(d/) = %v; want it to include the empty subdir d/empty", got)
	}
}

// TestExpandDirInputs_AmortizedAcrossActions proves a directory input consumed
// by many actions in one build is walked once, not once per consuming action.
func TestExpandDirInputs_AmortizedAcrossActions(t *testing.T) {
	ctx := t.Context()
	hfs, root := newDigestHashFS(t)
	const nFiles = 30
	mkTreeForDigest(t, root, nFiles)

	const actions = 5
	expandDirInputsWalks.Store(0)
	for range actions {
		got := hfs.expandDirInputs(ctx, root, []path.Path{"gen/"})
		if len(got) != nFiles {
			t.Fatalf("expandDirInputs returned %d files; want %d", len(got), nFiles)
		}
	}
	if walks := expandDirInputsWalks.Load(); walks != 1 {
		t.Errorf("expandDirInputs walked the tree %d times for %d consumers; want 1 (not amortized across actions)", walks, actions)
	}
}

// TestExpandDirInputs_InvalidatedOnUpdate verifies that recording an output
// under a cached directory input drops the cached expansion, so the next
// consumer re-walks and sees the new file (a producer can finish after an
// earlier consumer expanded the same directory).
func TestExpandDirInputs_InvalidatedOnUpdate(t *testing.T) {
	ctx := t.Context()
	hfs, root := newDigestHashFS(t)
	mkTreeForDigest(t, root, 5)

	expandDirInputsWalks.Store(0)
	hfs.expandDirInputs(ctx, root, []path.Path{"gen/"}) // walk 1, caches
	hfs.expandDirInputs(ctx, root, []path.Path{"gen/"}) // cache hit, no walk
	if walks := expandDirInputsWalks.Load(); walks != 1 {
		t.Fatalf("before mutation: walks=%d; want 1 (second expand should hit cache)", walks)
	}

	data := blob.FromBytes(digest.SHA256, "newf", []byte("NEW"))
	if err := hfs.Update(ctx, root, []UpdateEntry{{
		Name:    "gen/sub0/newf",
		Entry:   &merkletree.Entry{Data: data},
		Mode:    0644,
		ModTime: time.Unix(1000, 0),
	}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	hfs.expandDirInputs(ctx, root, []path.Path{"gen/"}) // must re-walk
	if walks := expandDirInputsWalks.Load(); walks != 2 {
		t.Errorf("Update under a cached dir input did not invalidate it: walks=%d; want 2 (stale expansion)", walks)
	}
}

// TestExpandDirInputs_InvalidatedOnRemoveAll verifies removal under a cached
// directory input drops the cached expansion, so the next consumer re-walks
// and the removed file no longer appears.
func TestExpandDirInputs_InvalidatedOnRemoveAll(t *testing.T) {
	ctx := t.Context()
	hfs, root := newDigestHashFS(t)
	mkTreeForDigest(t, root, 6)

	first := hfs.expandDirInputs(ctx, root, []path.Path{"gen/"})
	victim := first[0]
	if !slices.Contains(first, victim) {
		t.Fatalf("setup: %q not in expansion %v", victim, first)
	}

	if err := hfs.RemoveAll(ctx, root, victim); err != nil {
		t.Fatalf("RemoveAll(%q): %v", victim, err)
	}

	second := hfs.expandDirInputs(ctx, root, []path.Path{"gen/"})
	if slices.Contains(second, victim) {
		t.Errorf("removed file %q still in expansion %v (stale cache after RemoveAll)", victim, second)
	}
}

// TestExpandDirInputs_InvalidatedOnParentRemoveAll verifies removal of a
// parent of a cached directory input drops the cached expansion.
func TestExpandDirInputs_InvalidatedOnParentRemoveAll(t *testing.T) {
	ctx := t.Context()
	hfs, root := newDigestHashFS(t)
	mkTreeForDigest(t, root, 5)

	expandDirInputsWalks.Store(0)
	hfs.expandDirInputs(ctx, root, []path.Path{"gen/sub0/"})
	if err := hfs.RemoveAll(ctx, root, path.Path("gen")); err != nil {
		t.Fatal(err)
	}
	hfs.expandDirInputs(ctx, root, []path.Path{"gen/sub0/"})
	if w := expandDirInputsWalks.Load(); w != 2 {
		t.Errorf("RemoveAll(gen) did not invalidate gen/sub0/: walks=%d; want 2", w)
	}
}

// TestExpandDirInputs_InvalidatedByAllMutations covers the remaining
// structural-mutation hooks (Remove, Forget, ForgetMissingsInDir) that must
// drop a cached directory-input expansion (Update and RemoveAll are above).
func TestExpandDirInputs_InvalidatedByAllMutations(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, hfs *HashFS, root, victim string)
	}{
		{"Remove", func(t *testing.T, hfs *HashFS, root, victim string) {
			if err := hfs.Remove(ctx, root, path.Path(victim)); err != nil {
				t.Fatal(err)
			}
		}},
		{"Forget", func(t *testing.T, hfs *HashFS, root, victim string) {
			hfs.Forget(ctx, root, []path.Path{path.Path(victim)})
		}},
		{"ForgetMissingsInDir", func(t *testing.T, hfs *HashFS, root, victim string) {
			hfs.ForgetMissingsInDir(ctx, root, "gen")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hfs, root := newDigestHashFS(t)
			mkTreeForDigest(t, root, 8)

			first := hfs.expandDirInputs(ctx, root, []path.Path{"gen/"})
			if len(first) == 0 {
				t.Fatal("empty expansion in setup")
			}
			// Warm the cache, then count only post-warm walks.
			expandDirInputsWalks.Store(0)
			hfs.expandDirInputs(ctx, root, []path.Path{"gen/"})
			if w := expandDirInputsWalks.Load(); w != 0 {
				t.Fatalf("%s: cache not warm, pre-mutation expand re-walked (walks=%d)", tc.name, w)
			}

			tc.mutate(t, hfs, root, string(first[0]))

			hfs.expandDirInputs(ctx, root, []path.Path{"gen/"})
			if w := expandDirInputsWalks.Load(); w != 1 {
				t.Errorf("%s did not invalidate the cached dir-input expansion: post-mutation walks=%d; want 1", tc.name, w)
			}
		})
	}
}

// BenchmarkExpandDirInputs measures the per-call cost of expanding a directory
// input on a warm cache (the per-consumer fan-out cost), reporting walks/op.
func BenchmarkExpandDirInputs(b *testing.B) {
	ctx := b.Context()
	hfs, root := newDigestHashFS(b)
	mkTreeForDigest(b, root, 1000)
	hfs.expandDirInputs(ctx, root, []path.Path{"gen/"}) // warm

	expandDirInputsWalks.Store(0)
	b.ReportAllocs()
	for b.Loop() {
		hfs.expandDirInputs(ctx, root, []path.Path{"gen/"})
	}
	b.ReportMetric(float64(expandDirInputsWalks.Load())/float64(b.N), "walks/op")
}

// BenchmarkEntries measures Entries for 1000 file inputs, without and
// with a directory input over the same files.
func BenchmarkEntries(b *testing.B) {
	const nFiles = 1000
	files := make([]path.Path, 0, nFiles)
	for i := range nFiles {
		files = append(files, path.Path(fmt.Sprintf("gen/sub%d/f%d.dat", i%8, i)))
	}
	for _, bc := range []struct {
		name   string
		inputs []path.Path
		want   int
	}{
		{"NoDirInput", files, nFiles},
		// Entries keeps the files listed twice, explicitly and in gen/.
		{"DirInput", append([]path.Path{"gen/"}, files...), 2 * nFiles},
	} {
		b.Run(bc.name, func(b *testing.B) {
			ctx := b.Context()
			hfs, root := newDigestHashFS(b)
			mkTreeForDigest(b, root, nFiles)
			ents, err := hfs.Entries(ctx, root, bc.inputs)
			if err != nil {
				b.Fatal(err)
			}
			if len(ents) != bc.want {
				b.Fatalf("Entries returned %d entries; want %d", len(ents), bc.want)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := hfs.Entries(ctx, root, bc.inputs); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func newDirInputCacheForBench(n int) *HashFS {
	hfs := &HashFS{}
	for i := range n {
		hfs.dirInputCache.Store(fmt.Sprintf("/work/out/soong/.intermediates/vendor/module%d/android_arm64/gen", i), []path.Path(nil))
	}
	hfs.dirInputCached.Store(true)
	return hfs
}

// TestInvalidateDirInputCacheAllocs checks that checking the cached
// directories against unrelated paths doesn't allocate.
func TestInvalidateDirInputCacheAllocs(t *testing.T) {
	hfs := newDirInputCacheForBench(100)
	p := "/work/out/soong/.intermediates/frameworks/base/core/java/android/app/Activity.java"
	// AllocsPerRun also counts allocations of goroutines left running
	// by other tests, so take the minimum of a few attempts.
	n := testing.AllocsPerRun(100, func() { hfs.invalidateDirInputCache(p) })
	for i := 0; i < 10 && n != 0; i++ {
		n = min(n, testing.AllocsPerRun(100, func() { hfs.invalidateDirInputCache(p) }))
	}
	if n != 0 {
		t.Errorf("invalidateDirInputCache allocs=%v; want 0", n)
	}
}

func BenchmarkInvalidateDirInputCache(b *testing.B) {
	hfs := newDirInputCacheForBench(100)
	p := "/work/out/soong/.intermediates/frameworks/base/core/java/android/app/Activity.java"
	b.ReportAllocs()
	for b.Loop() {
		hfs.invalidateDirInputCache(p)
	}
}
