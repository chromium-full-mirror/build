// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/sync/clockcache"
)

func TestDirSpec(t *testing.T) {
	dir := &rpb.Directory{
		Files:       []*rpb.FileNode{{Name: "f"}, {Name: "g"}},
		Directories: []*rpb.DirectoryNode{{Name: "d"}},
		Symlinks:    []*rpb.SymlinkNode{{Name: "s", Target: "f"}},
	}
	dd := digest.Digest{Hash: "0123", SizeBytes: 4}
	b := &Builder{path: NewPath("/ws", "out")}

	spec := b.dirSpec("a/b", dd, dir)
	want := []string{"a/b/f", "a/b/g", "a/b/d", "a/b/s"}
	if diff := cmp.Diff(want, spec.names); diff != "" {
		t.Errorf("dirSpec(%q).names diff -want +got:\n%s", "a/b", diff)
	}
	if got := b.dirSpec("a/b", dd, dir); got != spec {
		t.Errorf("dirSpec(%q, %v) was not shared", "a/b", dd)
	}
	if got := b.dirSpec("a/c", dd, dir); got == spec {
		t.Errorf("dirSpec(%q, %v) shared with %q", "a/c", dd, "a/b")
	}

	root := b.dirSpec("", dd, dir)
	if diff := cmp.Diff([]string{"f", "g", "d", "s"}, root.names); diff != "" {
		t.Errorf("dirSpec(%q).names diff -want +got:\n%s", "", diff)
	}

	// Without a digest there is no key, so nothing is shared.
	s1 := b.dirSpec("a/b", digest.Digest{}, dir)
	s2 := b.dirSpec("a/b", digest.Digest{}, dir)
	if s1 == s2 {
		t.Errorf("dirSpec with zero digest was shared")
	}
}

// TestNewDirSpecMatchDir checks the fields matchInputRoot passes to
// hashfs.MatchDir, for a directory with and without symlinks.
func TestNewDirSpecMatchDir(t *testing.T) {
	fdig := digest.Digest{Hash: "abcd", SizeBytes: 3}
	for _, tc := range []struct {
		name  string
		dname string
		dir   *rpb.Directory
		want  *dirSpec
	}{
		{
			// Only Entries can check symlinks, so no children.
			name:  "symlink",
			dname: "a/b",
			dir: &rpb.Directory{
				Files:       []*rpb.FileNode{{Name: "f", Digest: fdig.Proto()}},
				Directories: []*rpb.DirectoryNode{{Name: "d"}},
				Symlinks:    []*rpb.SymlinkNode{{Name: "s", Target: "f"}},
			},
			want: &dirSpec{
				names:   []string{"a/b/f", "a/b/d", "a/b/s"},
				nfiles:  1,
				fulldir: "/ws/a/b",
			},
		},
		{
			name:  "no_symlink_at_the_root",
			dname: "",
			dir: &rpb.Directory{
				Files:       []*rpb.FileNode{{Name: "f", Digest: fdig.Proto(), IsExecutable: true}},
				Directories: []*rpb.DirectoryNode{{Name: "d"}},
			},
			want: &dirSpec{
				names:   []string{"f", "d"},
				nfiles:  1,
				fulldir: "/ws",
				children: []hashfs.DirChild{
					{Name: "f", Digest: fdig, IsExecutable: true},
					{Name: "d"},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := newDirSpec("/ws", tc.dname, tc.dir)
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(dirSpec{})); diff != "" {
				t.Errorf("newDirSpec(%q) diff -want +got:\n%s", tc.dname, diff)
			}
		})
	}
}

// TestDirSpecBounded checks that a bounded dirSpecs derives an evicted
// dirSpec again, equal to the first one.
func TestDirSpecBounded(t *testing.T) {
	b := &Builder{path: NewPath("/ws", "out")}
	b.dirSpecs.Init(1024, dirSpecBytes)
	dir := &rpb.Directory{
		Files:       []*rpb.FileNode{{Name: "f", Digest: digest.Digest{Hash: "abcd", SizeBytes: 3}.Proto()}},
		Directories: []*rpb.DirectoryNode{{Name: "d"}},
	}
	dd := digest.Digest{Hash: "0123", SizeBytes: 4}
	first := b.dirSpec("a/b", dd, dir)
	for i := range 20 {
		b.dirSpec(fmt.Sprintf("x/%d", i), dd, dir)
	}
	if st := b.DirSpecCacheStats(); st.Evictions == 0 || st.Bytes > 1024 {
		t.Fatalf("DirSpecCacheStats()=%+v; want Evictions > 0, Bytes <= 1024", st)
	}
	got := b.dirSpec("a/b", dd, dir)
	if got == first {
		t.Errorf("dirSpec(%q) was not evicted", "a/b")
	}
	if diff := cmp.Diff(first, got, cmp.AllowUnexported(dirSpec{})); diff != "" {
		t.Errorf("dirSpec(%q) after eviction diff -first +got:\n%s", "a/b", diff)
	}
}

// TestDirSpecBytesHeap checks that dirSpecBytes is within 10% of the heap
// that a cache of dirSpecs keeps after their Directories are gone, as when
// walkdirCache has evicted them.
func TestDirSpecBytesHeap(t *testing.T) {
	const n = 20000
	var c clockcache.Cache[dirSpecKey, *dirSpec]
	c.Init(1<<40, dirSpecBytes)
	runtime.GC()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range n {
		dir := &rpb.Directory{}
		for j := range 8 {
			name := fmt.Sprintf("file%d_%d.h", j, i)
			dir.Files = append(dir.Files, &rpb.FileNode{Name: name, Digest: digest.SHA256.FromBytes([]byte(name)).Proto()})
		}
		for j := range 2 {
			name := fmt.Sprintf("sub%d_%d", j, i)
			dir.Directories = append(dir.Directories, &rpb.DirectoryNode{Name: name, Digest: digest.SHA256.FromBytes([]byte(name)).Proto()})
		}
		b, err := proto.Marshal(dir)
		if err != nil {
			t.Fatal(err)
		}
		decoded := &rpb.Directory{}
		if err := proto.Unmarshal(b, decoded); err != nil {
			t.Fatal(err)
		}
		dname := fmt.Sprintf("prebuilts/clang/host/linux-x86/clang-r1/include/c++/v1/d%06d", i)
		key := dirSpecKey{dname: dname, dd: digest.SHA256.FromBytes(b)}
		c.LoadOrStore(key, newDirSpec("/ws", dname, decoded))
	}
	runtime.GC()
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	heap := float64(after.HeapAlloc) - float64(before.HeapAlloc)
	est := float64(c.Stats().Bytes)
	if r := heap / est; r < 0.9 || r > 1.1 {
		t.Errorf("heap %.0f bytes for estimate %.0f (ratio %.2f); want within 10%%", heap, est, r)
	}
	runtime.KeepAlive(&c)
}
