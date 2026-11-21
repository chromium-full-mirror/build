// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package merkletree operates on a merkle tree for remote execution API.
//
// You can find the Tree proto in REAPI here:
// https://github.com/bazelbuild/remote-apis/blob/c1c1ad2c97ed18943adb55f06657440daa60d833/build/bazel/remote/execution/v2/remote_execution.proto#L838
// See also https://en.Wikipedia.org/wiki/Merkle_tree for general explanations about Merkle tree.
package merkletree

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/path"
)

// MerkleTree represents a merkle tree.
type MerkleTree struct {
	// dirname to Directory.
	// empty dirname is root.
	m     map[string]*rpb.Directory
	fn    digest.Function
	store *blob.Store

	// Note: this cache showed a reduction of 7GB (4%) during a Chrome
	// build. The assumption is that traversal is sequential. A measurement
	// showed 161172 "cache hits" during a build of the `base` target.
	lastDir dirstate

	pooled bool

	// hasSymlinks is true once a symlink was set. Build only needs to
	// resolve symlink dirs when it is true.
	hasSymlinks bool

	// buf is scratch space to marshal a Directory when there is no store.
	buf []byte

	// arena allocates the nodes of a pooled tree. It is nil for trees
	// from New.
	arena *arena
}

// arena allocates proto nodes in chunks for a pooled MerkleTree.
type arena struct {
	files    chunks[rpb.FileNode]
	digests  chunks[rpb.Digest]
	dirNodes chunks[rpb.DirectoryNode]
	symlinks chunks[rpb.SymlinkNode]
	dirs     chunks[rpb.Directory]

	// names maps reused by buildTree, one per recursion depth.
	names []map[string]proto.Message
	depth int
}

// maxPooledNames is the most names a directory can have to use a pooled
// names map.
const maxPooledNames = 4096

const arenaChunk = 256

// arenaMaxRetained is the most nodes of one kind that a tree can have to
// go back to the pool.
const arenaMaxRetained = 1 << 16

// maxPooledBuf is the capacity above which Release drops the marshal
// buffer rather than keep it in the pool.
const maxPooledBuf = 1 << 20

// chunks is a chunked slab of T. Pointers to elements stay valid until reset.
type chunks[T any] struct {
	c [][]T
	n int // number of elements handed out
}

func (c *chunks[T]) alloc() *T {
	i, j := c.n/arenaChunk, c.n%arenaChunk
	if i == len(c.c) {
		c.c = append(c.c, make([]T, arenaChunk))
	}
	c.n++
	return &c.c[i][j]
}

// reset zeroes the elements handed out so far, so they don't retain
// anything and are ready for reuse.
func (c *chunks[T]) reset() {
	for i := 0; c.n > 0; i++ {
		k := min(c.n, arenaChunk)
		clear(c.c[i][:k])
		c.n -= k
	}
}

func (c *chunks[T]) size() int { return len(c.c) * arenaChunk }

// reset resets the arena for reuse.
func (a *arena) reset() {
	a.files.reset()
	a.digests.reset()
	a.dirNodes.reset()
	a.symlinks.reset()
	a.dirs.reset()
}

// retainable reports whether the arena is small enough to keep in the pool.
func (a *arena) retainable() bool {
	return a.files.size() <= arenaMaxRetained && a.digests.size() <= arenaMaxRetained &&
		a.dirNodes.size() <= arenaMaxRetained && a.dirs.size() <= arenaMaxRetained &&
		a.symlinks.size() <= arenaMaxRetained
}

func (m *MerkleTree) newFileNode() *rpb.FileNode {
	if m.arena == nil {
		return &rpb.FileNode{}
	}
	return m.arena.files.alloc()
}

func (m *MerkleTree) newSymlinkNode() *rpb.SymlinkNode {
	if m.arena == nil {
		return &rpb.SymlinkNode{}
	}
	return m.arena.symlinks.alloc()
}

func (m *MerkleTree) newDirectoryNode() *rpb.DirectoryNode {
	if m.arena == nil {
		return &rpb.DirectoryNode{}
	}
	return m.arena.dirNodes.alloc()
}

func (m *MerkleTree) newDirectory() *rpb.Directory {
	if m.arena == nil {
		return &rpb.Directory{}
	}
	return m.arena.dirs.alloc()
}

// getNames returns an empty map for buildTree's duplicate check; putNames
// must be called when the caller is done with it.
func (m *MerkleTree) getNames(n int) map[string]proto.Message {
	if m.arena == nil {
		return make(map[string]proto.Message, n)
	}
	a := m.arena
	if a.depth == len(a.names) {
		a.names = append(a.names, nil)
	}
	a.depth++
	if n > maxPooledNames {
		return make(map[string]proto.Message, n)
	}
	if a.names[a.depth-1] == nil {
		a.names[a.depth-1] = make(map[string]proto.Message, n)
	}
	return a.names[a.depth-1]
}

func (m *MerkleTree) putNames() {
	if m.arena == nil {
		return
	}
	m.arena.depth--
	clear(m.arena.names[m.arena.depth])
}

// digestProto returns d as a digest proto (nil for a zero digest, like d.Proto()).
func (m *MerkleTree) digestProto(d digest.Digest) *rpb.Digest {
	if m.arena == nil || d.IsZero() {
		return d.Proto()
	}
	p := m.arena.digests.alloc()
	p.Hash = d.Hash
	p.SizeBytes = d.SizeBytes
	return p
}

// New creates new merkle tree with digest function and digest store.
func New(fn digest.Function, store *blob.Store) *MerkleTree {
	return &MerkleTree{
		m: map[string]*rpb.Directory{
			"": {},
		},
		fn:    fn,
		store: store,
	}
}

var merkleTreePool = sync.Pool{
	New: func() any {
		return &MerkleTree{
			m: make(map[string]*rpb.Directory),
		}
	},
}

// NewPooled returns a MerkleTree from the pool.
// Call Release when done to return it; defer is the typical usage.
func NewPooled(fn digest.Function, store *blob.Store) *MerkleTree {
	m := merkleTreePool.Get().(*MerkleTree)
	m.fn = fn
	m.store = store
	m.lastDir = dirstate{}
	m.pooled = true
	if m.arena == nil {
		m.arena = &arena{}
	}
	m.m[""] = m.newDirectory()
	return m
}

// Release returns the MerkleTree to the pool, or drops it if it has more
// than arenaMaxRetained nodes of one kind.
// The MerkleTree must not be used after calling Release.
// Release is idempotent: calls after the first are no-ops, and it is a
// no-op for MerkleTrees created via New.
func (m *MerkleTree) Release() {
	if !m.pooled {
		return
	}
	m.pooled = false
	if m.arena != nil && !m.arena.retainable() {
		return
	}
	clear(m.m)
	m.store = nil
	m.lastDir = dirstate{}
	m.hasSymlinks = false
	if cap(m.buf) > maxPooledBuf {
		m.buf = nil
	}
	if m.arena != nil {
		m.arena.reset()
	}
	merkleTreePool.Put(m)
}

// Entry is an entry in the tree.
type Entry struct {
	// Name is relative path from root dir.
	// it might not be clean path.
	// 'dir1/../dir2/file' will create
	//  - 'dir1/'
	//  - 'dir2/'
	//  - 'dir2/file'
	// error if name goes out to root.
	Name path.Path

	// Data is entry's content. `nil` for directories and symlinks.
	Data blob.Data

	// IsExecutable is true if the file is executable.
	// no need to set this for directory.
	IsExecutable bool

	// If the file is a symlink, then this should be set to the target of the symlink.
	Target string
}

// IsDir returns true when the entry is a directory.
func (e Entry) IsDir() bool {
	return e.Data.IsZero() && e.Target == ""
}

// IsSymlink returns true when the entry is a symlink.
func (e Entry) IsSymlink() bool {
	return e.Data.IsZero() && e.Target != ""
}

var (
	// ErrAbsPath indicates name in Entry is absolute path.
	ErrAbsPath = errors.New("merkletree: absolute path name")

	// ErrAmbigFileSymlink indicates Entry has both `Data` and `Target` fields, cannot determine
	// whether it is File or Symlink.
	ErrAmbigFileSymlink = errors.New("merkletree: unable to determine file vs symlink")

	// ErrBadPath indicates name in Entry contains bad path component
	// e.g. "." or "..".
	ErrBadPath = errors.New("merkletree: bad path component")

	// ErrBadTree indicates TreeEntry has bad digest.
	ErrBadTree = errors.New("merkletree: bad tree")

	// ErrPrecomputedSubTree indicates to set an entry in precomputed subtree, but we can't change anything under the subtree because it used precomputed digest.
	ErrPrecomputedSubTree = errors.New("merkletree: set in precomputed subtree")
)

type dirstate struct {
	name string
	dir  *rpb.Directory
}

// Set sets an entry.
// It may return ErrAbsPath/ErrAmbigFileSymlink/ErrBadPath as error.
func (m *MerkleTree) Set(entry Entry) error {
	fname := string(entry.Name)
	if entry.Target != "" && !entry.Data.IsZero() {
		return fmt.Errorf("set %s: %w", fname, ErrAmbigFileSymlink)
	}
	if filepath.IsAbs(fname) || strings.HasPrefix(fname, "/") || strings.HasPrefix(fname, `\`) {
		return fmt.Errorf("set %s: %w", fname, ErrAbsPath)
	}
	if entry.IsDir() || entry.Target != "" {
		if _, exists := m.m[fname]; exists {
			return nil
		}
	}
	cur := dirstate{
		name: ".",
		dir:  m.RootDirectory(),
	}
	rest := fname
	// Try to reuse the cached directory state. Hand-check the prefix
	// and '/' separator instead of CutPrefix(fname, name+"/"), whose
	// concatenation allocates per call (~18M allocs per cache-cold build).
	var found bool
	dotdot := strings.Contains(fname, "..")
	if m.lastDir.dir != nil && !dotdot {
		ln := len(m.lastDir.name)
		if len(fname) > ln && fname[ln] == '/' && fname[:ln] == m.lastDir.name {
			cur = m.lastDir
			rest = fname[ln+1:]
			found = true
		}
	}
	if !found {
		m.lastDir = dirstate{}
	}
	// dirstack is needed only to resolve "..".
	var dirstack []dirstate
	for {
		name, next, found := strings.Cut(rest, "/")
		rest = next
		if !found {
			// a leaf.
			if entry.Data.IsZero() {
				if name == "" {
					return fmt.Errorf("set %s: empty path element: %w", fname, ErrBadPath)
				}
				if name == "." {
					return nil
				}
				if name == ".." && len(dirstack) == 0 {
					return fmt.Errorf("set %s: outside of workspace: %w", fname, ErrBadPath)
				}
				if name == ".." {
					return nil
				}
				m.lastDir = cur
				if entry.IsSymlink() {
					m.hasSymlinks = true
					sn := m.newSymlinkNode()
					sn.Name = name
					sn.Target = entry.Target
					cur.dir.Symlinks = append(cur.dir.Symlinks, sn)
					return nil
				}
				_, err := m.setDir(cur, name)
				if err != nil {
					return err
				}
				return nil
			}
			if name == "." || name == ".." {
				return fmt.Errorf("set %s: unexpected %s: %w", fname, name, ErrBadPath)
			}
			if m.store != nil {
				m.store.Set(entry.Data)
			}
			// A duplicate FileNode with this name may be appended here.
			// The REAPI Directory invariant (no two nodes share a name)
			// is enforced in buildTree, which collapses identical re-Sets
			// and errors on conflicting ones (different digest or
			// executable bit). Checking for an existing FileNode here
			// would add per-Set work to this hot path (a linear scan or a
			// per-directory map, both allocating) without changing the
			// built proto.
			fn := m.newFileNode()
			fn.Name = name
			fn.Digest = m.digestProto(entry.Data.Digest())
			fn.IsExecutable = entry.IsExecutable
			cur.dir.Files = append(cur.dir.Files, fn)
			m.lastDir = cur
			return nil
		}

		if name == "" {
			continue
		}
		if name == "." {
			continue
		}
		if name == ".." {
			if len(dirstack) == 0 {
				return fmt.Errorf("set %s: outside of workspace: %w", fname, ErrBadPath)
			}
			cur, dirstack = dirstack[len(dirstack)-1], dirstack[:len(dirstack)-1]
			continue
		}
		if dotdot {
			dirstack = append(dirstack, cur)
		}
		var err error
		cur, err = m.setDir(cur, name)
		if err != nil {
			// setDir only returns ErrPrecomputedSubTree, which every
			// caller errors.Is-checks and ignores. Return it bare;
			// wrapping it interpolates fname and allocates a string
			// per input descending into a precomputed subtree, all
			// discarded.
			if errors.Is(err, ErrPrecomputedSubTree) {
				return err
			}
			return fmt.Errorf("set %s: %w", fname, err)
		}
	}
}

func pathJoin(dir, base string) string {
	if dir == "." || dir == "" {
		return base
	}
	return dir + "/" + base
}

func (m *MerkleTree) setDir(cur dirstate, name string) (dirstate, error) {
	dirname := pathJoin(cur.name, name)
	dir, exists := m.m[dirname]
	if !exists {
		dirnode := m.newDirectoryNode()
		dirnode.Name = name // compute digest later
		cur.dir.Directories = append(cur.dir.Directories, dirnode)
		dir = m.newDirectory()
		m.m[dirname] = dir
	}
	if dir == nil {
		return dirstate{}, ErrPrecomputedSubTree
	}
	return dirstate{name: dirname, dir: dir}, nil
}

// TreeEntry is a precomputed subtree entry in the tree.
// When using the same sets in a directory many times, we can precompute
// digest for the subtree to reduce computation cost and memory cost.
type TreeEntry struct {
	// Name is relative path from root dir.
	// Name should not end with "." or "..".
	Name path.Path

	// Digest is the digest of this sub tree.
	Digest digest.Digest

	// Store is the digest store used to build the subtree.
	// It may be nil, if it is sure that the subtree was uploaded
	// to CAS and no need to check missing blobs / upload blobs
	// of the subtree.
	Store *blob.Store
}

// SetTree sets a subtree entry.
// It may return ErrAbsPath/ErrBadPath/ErrBadTree as error.
func (m *MerkleTree) SetTree(tentry TreeEntry) error {
	dname := string(tentry.Name)
	if tentry.Digest.IsZero() {
		return fmt.Errorf("setTree %s: %w", dname, ErrBadTree)
	}
	if filepath.IsAbs(dname) || strings.HasPrefix(dname, "/") || strings.HasPrefix(dname, `\`) {
		return fmt.Errorf("setTree %s: %w", dname, ErrAbsPath)
	}
	_, exists := m.m[dname]
	if exists {
		return fmt.Errorf("setTree %s: %w", dname, ErrPrecomputedSubTree)
	}
	cur := dirstate{
		name: ".",
		dir:  m.RootDirectory(),
	}
	// dirstack is needed only to resolve "..".
	dotdot := strings.Contains(dname, "..")
	var dirstack []dirstate
	rest := dname
	for {
		name, next, found := strings.Cut(rest, "/")
		rest = next
		if !found {
			// a leaf.
			if name == "" {
				return fmt.Errorf("setTree %s: empty path element: %w", dname, ErrBadPath)
			}
			if name == "." || name == ".." {
				return fmt.Errorf("setTree %s: %s at the leaf: %w", dname, name, ErrBadPath)
			}
			m.setTree(cur, name, tentry.Digest, tentry.Store)
			return nil
		}
		if name == "" {
			continue
		}
		if name == "." {
			continue
		}
		if name == ".." {
			if len(dirstack) == 0 {
				return fmt.Errorf("setTree %s: outside of workspace: %w", dname, ErrBadPath)
			}
			cur, dirstack = dirstack[len(dirstack)-1], dirstack[:len(dirstack)-1]
			continue
		}
		if dotdot {
			dirstack = append(dirstack, cur)
		}
		var err error
		cur, err = m.setDir(cur, name)
		if err != nil {
			return fmt.Errorf("setTree %s: %w", dname, err)
		}
	}
}

func (m *MerkleTree) setTree(cur dirstate, name string, d digest.Digest, store *blob.Store) {
	dirname := pathJoin(cur.name, name)
	dirnode := m.newDirectoryNode()
	dirnode.Name = name
	dirnode.Digest = m.digestProto(d)
	cur.dir.Directories = append(cur.dir.Directories, dirnode)
	_, exists := m.m[dirname]
	if !exists {
		m.m[dirname] = nil
	}
	if m.store != nil && store != nil {
		ds := store.List()
		for _, d := range ds {
			data, ok := store.Get(d)
			if !ok {
				continue
			}
			m.store.Set(data)
		}
	}
}

// Build builds merkle tree and returns root's digest.
// The tree must not be used after Build returns an error.
func (m *MerkleTree) Build(ctx context.Context) (digest.Digest, error) {
	// invalidate lastDir cache since we are resolving symlinks and might modify the tree structure
	m.lastDir = dirstate{}

	if m.hasSymlinks {
		err := m.resolveSymlinkDir(ctx, m.RootDirectory(), "")
		if err != nil {
			return digest.Digest{}, err
		}
	}
	d, err := m.buildTree(ctx, m.RootDirectory(), "")
	if err != nil {
		return digest.Digest{}, err
	}
	return digest.FromProto(d), nil
}

// RootDirectory returns root directory in the merkle tree.
func (m *MerkleTree) RootDirectory() *rpb.Directory {
	return m.m[""]
}

// resolveSymlinkDir resolves symlink to directory.
// If "path/to/name" is symlink, target to "other/name",
// and "path/to/name" is directory, then the directory entries
// are merged into "path/to/other/name" directory.
func (m *MerkleTree) resolveSymlinkDir(ctx context.Context, curdir *rpb.Directory, dirname string) error {
	for _, s := range curdir.Symlinks {
		dirname := pathJoin(dirname, s.Name)
		dir, found := m.m[dirname]
		if !found {
			continue
		}
		realDirname := filepath.ToSlash(filepath.Join(filepath.Dir(dirname), s.Target))
		err := m.mergeDir(ctx, realDirname, dir)
		if err != nil {
			return err
		}
		// relocate subdirs under dirname to realDirname.
		for name, dir := range m.m {
			if strings.HasPrefix(name, dirname+"/") {
				tname := realDirname + strings.TrimPrefix(name, dirname)
				tdir, ok := m.m[tname]
				if ok {
					proto.Merge(tdir, dir)
				} else {
					tdir = dir
				}
				m.m[tname] = tdir
				delete(m.m, name)
			}
		}
	}
	for _, subdir := range curdir.Directories {
		dirname := pathJoin(dirname, subdir.Name)
		dir, found := m.m[dirname]
		if !found {
			continue
		}
		if dir != nil && subdir.Digest == nil {
			err := m.resolveSymlinkDir(ctx, dir, dirname)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// mergeDir merges dir into dirname's directory.
func (m *MerkleTree) mergeDir(ctx context.Context, dirname string, dir *rpb.Directory) error {
	cur := dirstate{
		name: ".",
		dir:  m.RootDirectory(),
	}
	rest := dirname
	for {
		name, next, found := strings.Cut(rest, "/")
		rest = next
		if !found {
			// leaf
			cur, err := m.setDir(cur, name)
			if err != nil {
				if errors.Is(err, ErrPrecomputedSubTree) {
					// build/mac_files/SDKs/MacOSX.sdk will hit this.
					clog.Warningf(ctx, "mergeDir set %s: %v", dirname, err)
					return nil
				}
				return fmt.Errorf("mergeDir: set %s: %w", dirname, err)
			}
			cur.dir.Files = append(cur.dir.Files, dir.Files...)
			cur.dir.Symlinks = append(cur.dir.Symlinks, dir.Symlinks...)
			cur.dir.Directories = append(cur.dir.Directories, dir.Directories...)
			return nil
		}

		var err error
		cur, err = m.setDir(cur, name)
		if err != nil {
			if errors.Is(err, ErrPrecomputedSubTree) {
				// files under this dir should exist in precomputed tree.
				clog.Warningf(ctx, "mergeDir set %s: %v", dirname, err)
				return nil
			}
			return fmt.Errorf("mergeDir: set %s: %w", dirname, err)
		}
	}
}

// buildtree builds tree at curdir, which is located as dirname.
func (m *MerkleTree) buildTree(ctx context.Context, curdir *rpb.Directory, dirname string) (*rpb.Digest, error) {
	// A Directory must not carry two nodes sharing one name (REAPI
	// invariant, http://b/124693412): identical re-entries collapse to
	// one, conflicting entries are an error. Exception: a directory
	// sharing a name with a symlink is skipped (the symlink wins); see
	// TestBuildDuplicateSymlinkDir.
	names := m.getNames(len(curdir.Files) + len(curdir.Symlinks) + len(curdir.Directories))
	defer m.putNames()
	// Filter in place: the write index never passes the read index.
	files := curdir.Files[:0]
	for _, f := range curdir.Files {
		p, found := names[f.Name]
		if found {
			if !proto.Equal(f, p) {
				return nil, fmt.Errorf("duplicate file %s in %s: %s != %s", f.Name, dirname, f, p)
			}
			// Siso might send duplicate entries such as
			//   dir/subdir/../otherdir/name
			//   dir/otherdir/name
			clog.Infof(ctx, "duplicate file %s in %s: %s", f.Name, dirname, f)
			continue
		}
		names[f.Name] = f
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})
	curdir.Files = files

	symlinks := curdir.Symlinks[:0]
	for _, s := range curdir.Symlinks {
		p, found := names[s.Name]
		if found {
			if !proto.Equal(s, p) {
				return nil, fmt.Errorf("duplicate symlink %s in %s: %s != %s", s.Name, dirname, s, p)
			}
			clog.Infof(ctx, "duplicate symlink %s in %s: %s", s.Name, dirname, s)
			continue
		}
		names[s.Name] = s
		symlinks = append(symlinks, s)
	}
	sort.Slice(symlinks, func(i, j int) bool {
		return symlinks[i].Name < symlinks[j].Name
	})
	curdir.Symlinks = symlinks

	dirs := curdir.Directories[:0]
	for _, subdir := range curdir.Directories {
		subdirname := pathJoin(dirname, subdir.Name)
		dir, found := m.m[subdirname]
		if !found {
			return nil, fmt.Errorf("directory not found: %s", subdirname)
		}
		p, found := names[subdir.Name]
		if found {
			switch p.(type) {
			case *rpb.SymlinkNode:
				clog.Infof(ctx, "use symlink for dir: %s", subdirname)
				continue
			}
			// other checks later.
		}
		if dir != nil && subdir.Digest == nil {
			digest, err := m.buildTree(ctx, dir, subdirname)
			if err != nil {
				return nil, err
			}
			subdir.Digest = digest
		}
		if found {
			if !proto.Equal(subdir, p) {
				return nil, fmt.Errorf("duplicate dir %s in %s: %s != %s", subdir.Name, dirname, subdir, p)
			}
			clog.Infof(ctx, "duplicate dir %s in %s: %s", subdir.Name, dirname, subdir)
			continue
		}
		names[subdir.Name] = subdir
		dirs = append(dirs, subdir)
	}
	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].Name < dirs[j].Name
	})
	curdir.Directories = dirs

	if m.store == nil {
		// Only the digest is needed: marshal into the scratch buffer.
		buf, err := proto.MarshalOptions{}.MarshalAppend(m.buf[:0], curdir)
		if err != nil {
			return nil, fmt.Errorf("directory digest %s: %w", dirname, err)
		}
		m.buf = buf
		return m.digestProto(m.fn.FromBytes(buf)), nil
	}
	data, err := blob.FromProtoMessage(m.fn, curdir)
	if err != nil {
		return nil, fmt.Errorf("directory digest %s: %w", dirname, err)
	}
	if m.store != nil {
		m.store.Set(data)
	}
	return m.digestProto(data.Digest()), nil
}

// Directories returns directories in the merkle tree.
func (m *MerkleTree) Directories() []*rpb.Directory {
	dirs := make([]*rpb.Directory, 0, len(m.m))
	for _, d := range m.m {
		dirs = append(dirs, d)
	}
	return dirs
}
