// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"path/filepath"
	"unsafe"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/sync/clockcache"
)

// dirSpecMemLimitDivisor makes the default dirSpecs cap 1/20 of
// GOMEMLIMIT (see clockcache.ResolveBytes).
const dirSpecMemLimitDivisor = 20

// dirSpecKey identifies one REAPI Directory at one path of an input root.
type dirSpecKey struct {
	dname string
	dd    digest.Digest
}

// dirSpec is what matchInputRoot derives from a Directory and its path
// alone. A REAPI Directory is content addressed, so a dirSpec is a pure
// function of its dirSpecKey: it holds nothing about local state and never
// becomes stale.
//
// Input roots share most of their directories. On an Android build,
// matchInputRoot visited directories 112M times, but only ~1.8M distinct
// ones, so deriving this once per key instead of once per visit removes most
// of the path joining and allocation from the walk.
type dirSpec struct {
	// names is the slash-separated path of every entry: the files, then
	// the directories, then the symlinks, each in Directory order.
	names []string

	// nfiles is the number of files; names[:nfiles] are their paths.
	nfiles int

	// fulldir is the directory's full path in hashfs.
	fulldir string

	// children is what hashfs.MatchDir checks: every file and
	// directory. It is nil if the Directory has symlinks, which only
	// Entries can check.
	children []hashfs.DirChild
}

func newDirSpec(root, dname string, dir *rpb.Directory) *dirSpec {
	s := &dirSpec{
		names:   make([]string, 0, len(dir.Files)+len(dir.Directories)+len(dir.Symlinks)),
		nfiles:  len(dir.Files),
		fulldir: string(path.JoinRoot(root, path.Path(dname))),
	}
	for _, file := range dir.Files {
		s.names = append(s.names, filepath.ToSlash(filepath.Join(dname, file.Name)))
	}
	for _, subdir := range dir.Directories {
		s.names = append(s.names, filepath.ToSlash(filepath.Join(dname, subdir.Name)))
	}
	for _, symlink := range dir.Symlinks {
		s.names = append(s.names, filepath.ToSlash(filepath.Join(dname, symlink.Name)))
	}
	if len(dir.Symlinks) == 0 {
		s.children = make([]hashfs.DirChild, 0, len(dir.Files)+len(dir.Directories))
		for _, file := range dir.Files {
			s.children = append(s.children, hashfs.DirChild{
				Name:         file.Name,
				Digest:       digest.FromProto(file.Digest),
				IsExecutable: file.IsExecutable,
			})
		}
		for _, subdir := range dir.Directories {
			s.children = append(s.children, hashfs.DirChild{Name: subdir.Name})
		}
	}
	return s
}

// dirSpecBytes estimates the heap retained by caching s at key,
// including the names and hashes it shares with the Directory.
func dirSpecBytes(key dirSpecKey, s *dirSpec) int64 {
	// dirSpec (80), and the cache entry, sync.Map nodes and boxed key.
	n := 310 + allocBytes(len(key.dname)) + allocBytes(len(key.dd.Hash)) + allocBytes(len(s.fulldir))
	n += allocBytes(int(unsafe.Sizeof("")) * cap(s.names))
	for _, name := range s.names {
		n += allocBytes(len(name))
	}
	n += allocBytes(int(unsafe.Sizeof(hashfs.DirChild{})) * cap(s.children))
	for _, c := range s.children {
		n += allocBytes(len(c.Name)) + allocBytes(len(c.Digest.Hash))
	}
	return n
}

// allocBytes approximates the heap that an allocation of n bytes takes,
// rounded up like Go's size classes.
func allocBytes(n int) int64 {
	switch {
	case n <= 0:
		return 0
	case n <= 8:
		return 8
	case n <= 256:
		return int64((n + 15) &^ 15)
	case n <= 1024:
		return int64((n + 63) &^ 63)
	default:
		return int64((n + 127) &^ 127)
	}
}

// dirSpec returns the dirSpec of dir at dname, whose digest is dd.
// A bounded dirSpecs may have evicted it; it is then derived again.
func (b *Builder) dirSpec(dname string, dd digest.Digest, dir *rpb.Directory) *dirSpec {
	root := b.path.WorkspaceRoot
	if dd.IsZero() {
		// No key; never share.
		return newDirSpec(root, dname, dir)
	}
	key := dirSpecKey{dname: dname, dd: dd}
	if s, ok := b.dirSpecs.Load(key); ok {
		return s
	}
	return b.dirSpecs.LoadOrStore(key, newDirSpec(root, dname, dir))
}

// DirSpecCacheStats returns the counters of the cache of what two-phase
// caching derives from input root directories.
func (b *Builder) DirSpecCacheStats() clockcache.Stats {
	return b.dirSpecs.Stats()
}
