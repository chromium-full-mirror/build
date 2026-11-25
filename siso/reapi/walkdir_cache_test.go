// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package reapi_test

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/reapi"
	"go.chromium.org/build/siso/reapi/reapitest"
	"go.chromium.org/build/siso/sync/clockcache"
)

// newWalkDirClient returns a client whose CAS holds a small tree
// with 5 directories, and the tree's root digest.
func newWalkDirClient(ctx context.Context, t *testing.T, opt reapi.Option) (*reapi.Client, digest.Digest) {
	t.Helper()
	cl := reapitest.NewWithOption(ctx, t, &reapitest.Fake{}, opt)
	return cl, uploadTree(ctx, t, cl, "file1", "a/file1", "b/file1", "b/c/file1", "b/d/file2")
}

// TestWalkDirSharesCachedDirectories checks that walks served from the
// cache hand out the cached messages themselves, and that walking does
// not modify them.
func TestWalkDirSharesCachedDirectories(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, cacheBytes := range []int64{-1, 1 << 20} {
			t.Run(fmt.Sprintf("stream=%t_cacheBytes=%d", stream, cacheBytes), func(t *testing.T) {
				testWalkDirSharesCachedDirectories(t, stream, cacheBytes)
			})
		}
	}
}

func testWalkDirSharesCachedDirectories(t *testing.T, stream bool, cacheBytes int64) {
	ctx := t.Context()
	cl, d := newWalkDirClient(ctx, t, reapi.Option{
		WalkDirStream:     stream,
		WalkDirCacheBytes: cacheBytes,
	})
	walk := func() map[string]*rpb.Directory {
		t.Helper()
		dirs := make(map[string]*rpb.Directory)
		err := cl.WalkDir(ctx, d, func(dname string, _ digest.Digest, dir *rpb.Directory) error {
			dirs[dname] = dir
			return nil
		})
		if err != nil {
			t.Fatalf("WalkDir=%v; want nil", err)
		}
		return dirs
	}
	first := walk()
	orig := make(map[string]*rpb.Directory)
	for dname, dir := range first {
		orig[dname] = proto.Clone(dir).(*rpb.Directory)
	}
	for range 2 {
		got := walk()
		if len(got) != len(first) {
			t.Errorf("walked %d dirs; want %d", len(got), len(first))
		}
		for dname, dir := range got {
			if dir != first[dname] {
				t.Errorf("%q: cached walk returned a different message", dname)
			}
			if !proto.Equal(dir, orig[dname]) {
				t.Errorf("%q: directory modified by walk: %v; want %v", dname, dir, orig[dname])
			}
		}
	}
}

// TestWalkDirBoundedCacheEvictsAndRefetches checks that when WalkDirCacheBytes
// bounds the in-memory cache, evicted trees are cleanly re-fetched from CAS (or
// reloaded from LocalCache without CAS RPCs when configured).
func TestWalkDirBoundedCacheEvictsAndRefetches(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, withLocalCache := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t_localCache=%t", stream, withLocalCache), func(t *testing.T) {
				ctx := t.Context()
				var spy casSpy
				opt := reapi.Option{
					WalkDirStream: stream,
					// Small enough to hold ~2-3 directories, so walking tree2
					// evicts tree1 from the in-memory walkdirCache.
					WalkDirCacheBytes: 2048,
				}
				if withLocalCache {
					lc, err := reapi.NewLocalCache(t.TempDir())
					if err != nil {
						t.Fatalf("NewLocalCache: %v", err)
					}
					opt.LocalCache = lc
				}
				cl := reapitest.NewWithOption(ctx, t, &reapitest.Fake{}, opt, spy.dialOptions()...)

				d1 := uploadTree(ctx, t, cl, "t1/f1", "t1/a/f2", "t1/b/f3")
				d2 := uploadTree(ctx, t, cl, "t2/f1", "t2/a/f2", "t2/b/f3", "t2/c/f4")

				want1, err := walk(ctx, cl, d1)
				if err != nil {
					t.Fatalf("first walk(d1)=%v; want nil", err)
				}
				if _, err := walk(ctx, cl, d2); err != nil {
					t.Fatalf("walk(d2)=%v; want nil", err)
				}

				before := spy.reads.Load()
				got1, err := walk(ctx, cl, d1)
				if err != nil {
					t.Fatalf("second walk(d1)=%v; want nil", err)
				}
				if diff := cmp.Diff(want1, got1); diff != "" {
					t.Errorf("second walk(d1) diff -want +got:\n%s", diff)
				}
				reads := spy.reads.Load() - before
				if withLocalCache && reads != 0 {
					t.Errorf("second walk(d1) with LocalCache made %d CAS reads; want 0", reads)
				}
				if !withLocalCache && reads == 0 {
					t.Errorf("second walk(d1) without LocalCache made 0 CAS reads; want > 0 after eviction")
				}
				if st := cl.WalkDirCacheStats(); st.Evictions == 0 {
					t.Errorf("WalkDirCacheStats()=%+v; want Evictions > 0", st)
				}
			})
		}
	}
}

// TestWalkDirCacheUnboundedOption checks that a negative WalkDirCacheBytes
// keeps the cache unbounded.
func TestWalkDirCacheUnboundedOption(t *testing.T) {
	ctx := t.Context()
	var spy casSpy
	cl := reapitest.NewWithOption(ctx, t, &reapitest.Fake{}, reapi.Option{
		WalkDirStream:     true,
		WalkDirCacheBytes: -1,
	}, spy.dialOptions()...)
	d1 := uploadTree(ctx, t, cl, "t1/f1", "t1/a/f2")
	d2 := uploadTree(ctx, t, cl, "t2/f1", "t2/a/f2")
	if _, err := walk(ctx, cl, d1); err != nil {
		t.Fatalf("walk(d1)=%v; want nil", err)
	}
	if _, err := walk(ctx, cl, d2); err != nil {
		t.Fatalf("walk(d2)=%v; want nil", err)
	}
	before := spy.reads.Load()
	if _, err := walk(ctx, cl, d1); err != nil {
		t.Fatalf("second walk(d1)=%v; want nil", err)
	}
	if reads := spy.reads.Load() - before; reads != 0 {
		t.Errorf("second walk(d1) made %d CAS reads; want 0 with unbounded cache", reads)
	}
}

// TestWalkDirCacheFlagParse checks that RegisterFlags registers and parses
// -<prefix>_walkdir_cache_bytes for default, explicit positive, and negative values.
func TestWalkDirCacheFlagParse(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		args   []string
		want   int64
	}{
		{
			name: "default_zero",
			want: 0,
		},
		{
			name: "explicit_positive",
			args: []string{"-reapi_walkdir_cache_bytes=67108864"},
			want: 67108864,
		},
		{
			name: "explicit_unbounded_negative",
			args: []string{"-reapi_walkdir_cache_bytes=-1"},
			want: -1,
		},
		{
			name:   "custom_prefix",
			prefix: "cas",
			args:   []string{"-cas_walkdir_cache_bytes=16777216"},
			want:   16777216,
		},
	}
	for _, tc := range cases {
		var opt reapi.Option
		opt.Prefix = tc.prefix
		fs := flag.NewFlagSet(tc.name, flag.ContinueOnError)
		opt.RegisterFlags(fs, nil)
		if err := fs.Parse(tc.args); err != nil {
			t.Errorf("%s: Parse(%v)=%v; want nil", tc.name, tc.args, err)
			continue
		}
		if opt.WalkDirCacheBytes != tc.want {
			t.Errorf("%s: WalkDirCacheBytes=%d; want %d", tc.name, opt.WalkDirCacheBytes, tc.want)
		}
	}
}

// TestWalkDirCacheStats checks WalkDirCacheStats for a bounded and an
// unbounded cache.
func TestWalkDirCacheStats(t *testing.T) {
	t.Run("bounded", func(t *testing.T) {
		var logBuf bytes.Buffer
		ctx := clog.NewContext(t.Context(), clog.FromContext(t.Context()).WithWriter(&logBuf))
		cl := reapitest.NewWithOption(ctx, t, &reapitest.Fake{}, reapi.Option{
			WalkDirStream:     true,
			WalkDirCacheBytes: 2048,
		})
		d1 := uploadTree(ctx, t, cl, "t1/f1", "t1/a/f2", "t1/b/f3")
		d2 := uploadTree(ctx, t, cl, "t2/f1", "t2/a/f2", "t2/b/f3", "t2/c/f4")
		if _, err := walk(ctx, cl, d1); err != nil {
			t.Fatalf("walk(d1)=%v; want nil", err)
		}
		if _, err := walk(ctx, cl, d2); err != nil {
			t.Fatalf("walk(d2)=%v; want nil", err)
		}
		st := cl.WalkDirCacheStats()
		if st.CapBytes != 2048 || st.Bytes <= 0 || st.Bytes > 2048 || st.Entries == 0 || st.Evictions == 0 || st.Oversized != 0 {
			t.Errorf("WalkDirCacheStats()=%+v; want CapBytes=2048, Bytes in (0, 2048], Entries > 0, Evictions > 0, Oversized=0", st)
		}
		const wantLog = "walkdir cache limit: 2048 bytes"
		if logs := logBuf.String(); strings.Count(logs, wantLog) != 1 {
			t.Errorf("logs have %q %d times; want 1\nlogs:\n%s", wantLog, strings.Count(logs, wantLog), logs)
		}
	})

	t.Run("unbounded", func(t *testing.T) {
		ctx := t.Context()
		cl, d := newWalkDirClient(ctx, t, reapi.Option{
			WalkDirStream:     true,
			WalkDirCacheBytes: -1,
		})
		if _, err := walk(ctx, cl, d); err != nil {
			t.Fatalf("walk=%v; want nil", err)
		}
		if st := cl.WalkDirCacheStats(); st != (clockcache.Stats{}) {
			t.Errorf("WalkDirCacheStats()=%+v; want zero", st)
		}
	})
}

// TestWalkDirOversizedSubtree checks that a directory too large for a
// bounded cache is not stored, and that walks of its tree read the tree
// again and give the same directories.
func TestWalkDirOversizedSubtree(t *testing.T) {
	ctx := t.Context()
	var names []string
	for i := range 30 {
		names = append(names, fmt.Sprintf("file%02d", i))
	}
	sub21 := &rpb.Directory{Files: fileNodes("file1")}
	sub2 := &rpb.Directory{Files: fileNodes(names...), Directories: []*rpb.DirectoryNode{dirNode(t, "subdir2.1", sub21)}}
	sub1 := &rpb.Directory{Files: fileNodes("file1")}
	root := &rpb.Directory{Files: fileNodes("file1"), Directories: []*rpb.DirectoryNode{dirNode(t, "subdir1", sub1), dirNode(t, "subdir2", sub2)}}
	cas := &treeCAS{trees: map[digest.Digest][]*rpb.GetTreeResponse{
		dirDigest(t, root): {{Directories: []*rpb.Directory{root, sub1, sub2, sub21}}},
	}}
	// subdir2 has 30 files, so it is larger than 4096 bytes in the
	// cache; the other directories fit.
	cl := newTreeClientWithOption(t, cas, reapi.Option{WalkDirStream: true, WalkDirCacheBytes: 4096})

	want, err := walk(ctx, cl, dirDigest(t, root))
	if err != nil {
		t.Fatalf("first walk=%v; want nil", err)
	}
	for i := range 2 {
		got, err := walk(ctx, cl, dirDigest(t, root))
		if err != nil {
			t.Fatalf("walk %d=%v; want nil", i, err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("walk %d: diff -want +got:\n%s", i, diff)
		}
		if n, want := cas.calls.Load(), int32(i+2); n != want {
			t.Errorf("walk %d: %d GetTree calls; want %d", i, n, want)
		}
	}
	if st := cl.WalkDirCacheStats(); st.Oversized == 0 || st.Evictions != 0 {
		t.Errorf("WalkDirCacheStats()=%+v; want Oversized > 0, Evictions=0", st)
	}
}
