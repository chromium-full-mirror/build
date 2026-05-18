// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build linux

package localexec

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/model"
)

// fuseOwner holds the UID/GID that all FUSE inodes report as their owner.
// This must be the UID/GID of the process that runs Kajiya, so that the
// values are valid inside nsjail's user namespace (which maps only this
// UID/GID). If the inodes report UID/GID 0, overlayfs copy-up fails with
// EOVERFLOW because notify_change() rejects UIDs that are unmapped in the
// mount's user namespace.
var fuseOwner = fuse.Owner{
	Uid: uint32(os.Getuid()),
	Gid: uint32(os.Getgid()),
}

// CASRoot is the root node of the FUSE filesystem that serves CAS files.
// It dynamically presents per-action sandbox subtrees. Each sandbox is
// registered as a persistent inode child, with the entire input tree built
// from persistent inodes at registration time.
type CASRoot struct {
	fs.Inode
	mu        sync.Mutex
	sandboxes map[string]*fs.Inode

	// fileInodes caches persistent inodes for CAS files, keyed by CAS path.
	// dirInodes caches persistent inodes for directories, keyed by REAPI
	// directory digest. Sharing inodes across sandboxes means the kernel's
	// page cache, dentry cache, and readdir cache are all shared — accesses
	// in one sandbox warm caches for all others.
	fileInodes sync.Map // map[string]*fs.Inode (CAS path -> file inode)
	dirInodes  sync.Map // map[string]*fs.Inode (digest hash -> dir subtree inode)
}

var _ = (fs.NodeGetattrer)((*CASRoot)(nil))

func (r *CASRoot) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755
	out.Owner = fuseOwner
	return 0
}

// RegisterSandbox creates a virtual directory tree under the FUSE mount
// for the given InputTrie. File contents are served directly from CAS on
// demand. Returns the on-disk path within the FUSE mount to use as the
// overlayfs lower directory.
func (r *CASRoot) RegisterSandbox(sandboxID string, trie *model.DirectoryTrie, cas *blobstore.ContentAddressableStorage, fuseMountpoint string) (string, error) {
	ctx := context.Background()

	// Map from trie path to inode for building the tree.
	inodes := map[string]*fs.Inode{}

	var sandboxInode *fs.Inode
	var walkErr error

	trie.Root().Walk(func(k []byte, dir *model.KajiyaDirectory) bool {
		triePath := string(k) // e.g. "" for root, "src/" for a child.
		trieDir := strings.TrimSuffix(triePath, "/")

		if sandboxInode == nil {
			// The sandbox root is always per-sandbox (unique sandbox ID).
			sandboxInode = r.EmbeddedInode().NewPersistentInode(ctx, &casDir{
				attr: dirAttr(dir),
			}, fs.StableAttr{Mode: syscall.S_IFDIR})
			inodes[triePath] = sandboxInode
		}

		dirInode, ok := inodes[triePath]
		if !ok {
			walkErr = fmt.Errorf("parent inode not found for path %q", triePath)
			return true
		}

		// Check if we already have a cached inode subtree for this directory
		// digest. If so, reuse it: all files, symlinks, and subdirectories
		// are already children of the cached inode, so we only need to wire
		// up the inodes map for the trie walk.
		digestKey := dir.Digest.Hash
		if digestKey != "" && dirInode != sandboxInode {
			if cached, ok := r.dirInodes.Load(digestKey); ok {
				cachedInode := cached.(*fs.Inode)

				// The placeholder inode was added as a child of its parent
				// during the parent's subdir loop. Replace it with the
				// cached inode. The path is "parent/name/" so we derive
				// the child name from the last path component.
				childName := path.Base(trieDir)
				parentKey := path.Dir(trieDir)
				if parentKey == "." {
					// The parent of a root child such as "src/" is the
					// empty root key, not path.Dir's "." sentinel.
					parentKey = ""
				} else {
					parentKey += "/"
				}
				parentInode, ok := inodes[parentKey]
				if !ok {
					walkErr = fmt.Errorf("parent inode not found for cached path %q (parent %q)", triePath, parentKey)
					return true
				}
				parentInode.AddChild(childName, cachedInode, true)

				inodes[triePath] = cachedInode
				dirInode = cachedInode

				// Wire up subdirectory entries in the inodes map so the
				// trie walk can find children when it visits them next.
				for _, sd := range dir.Dirs {
					childKey := path.Join(trieDir, sd) + "/"
					if child := dirInode.GetChild(sd); child != nil {
						inodes[childKey] = child
					}
				}

				return false
			}
		}

		// Update attrs if the inode was pre-created by a parent's Dirs list
		// with default attributes.
		if dirOps, ok := dirInode.Operations().(*casDir); ok {
			dirOps.attr = dirAttr(dir)
		}

		// Add files. When inode caching is enabled, reuse existing inodes
		// for CAS files that are already known from other sandboxes, so the
		// kernel's page cache is shared across actions.
		for _, f := range dir.Files {
			casPath := cas.Path(f.Digest)
			newInode := dirInode.NewPersistentInode(ctx, &casFile{
				casPath: casPath,
				attr: fuse.Attr{
					Mode:      uint32(f.UnixMode),
					Size:      uint64(f.Digest.Size),
					Owner:     fuseOwner,
					Mtime:     uint64(f.Mtime.Unix()),
					Mtimensec: uint32(f.Mtime.Nanosecond()),
				},
			}, fs.StableAttr{Mode: syscall.S_IFREG})
			// Use LoadOrStore to avoid a TOCTOU race: if another
			// goroutine already cached an inode for this CAS path,
			// reuse it so the kernel shares page cache across sandboxes.
			actual, _ := r.fileInodes.LoadOrStore(casPath, newInode)
			dirInode.AddChild(f.Name, actual.(*fs.Inode), true)
		}

		// Add symlinks.
		for _, sl := range dir.Symlinks {
			slInode := dirInode.NewPersistentInode(ctx, &casSymlink{
				target: sl.Target,
			}, fs.StableAttr{Mode: syscall.S_IFLNK})
			dirInode.AddChild(sl.Name, slInode, true)
		}

		// Create subdirectory inodes. Their attributes will be updated when
		// the trie walk visits them (parent directories are always visited
		// before children due to lexicographic ordering with trailing slashes).
		// Reuse existing children from shared inodes (via dirInodes cache).
		for _, sd := range dir.Dirs {
			childKey := path.Join(trieDir, sd) + "/"
			if existing := dirInode.GetChild(sd); existing != nil {
				inodes[childKey] = existing
			} else {
				childInode := dirInode.NewPersistentInode(ctx, &casDir{}, fs.StableAttr{Mode: syscall.S_IFDIR})
				dirInode.AddChild(sd, childInode, true)
				inodes[childKey] = childInode
			}
		}

		// Cache this directory subtree for reuse by future sandboxes.
		if digestKey != "" && dirInode != sandboxInode {
			r.dirInodes.Store(digestKey, dirInode)
		}

		return false
	})

	if walkErr != nil {
		return "", walkErr
	}

	if sandboxInode == nil {
		return "", fmt.Errorf("empty input trie")
	}

	// Only hold the lock for the final attachment to the root inode and
	// map update. The subtree construction above is lock-free because each
	// sandbox operates on its own independent inode subtree.
	r.mu.Lock()
	r.EmbeddedInode().AddChild(sandboxID, sandboxInode, true)
	r.sandboxes[sandboxID] = sandboxInode
	r.mu.Unlock()

	return filepath.Join(fuseMountpoint, sandboxID), nil
}

// UnregisterSandbox removes the virtual directory tree for the given sandbox.
func (r *CASRoot) UnregisterSandbox(sandboxID string) {
	r.mu.Lock()
	if _, ok := r.sandboxes[sandboxID]; ok {
		r.EmbeddedInode().RmChild(sandboxID)
		delete(r.sandboxes, sandboxID)
	}
	r.mu.Unlock()

	// Only the sandbox root is detached. If inode caching is enabled, shared
	// directory and file subtrees remain alive in dirInodes/fileInodes for
	// reuse by other sandboxes. The sandbox root inode becomes unreachable
	// and will be reclaimed by go-fuse when its kernel lookup count drops
	// to zero.
}

// Close releases resources held by the CASRoot. Must be called before unmount.
func (r *CASRoot) Close() {
}

func dirAttr(dir *model.KajiyaDirectory) fuse.Attr {
	// Always include write permission for all users. The FUSE filesystem
	// serves as the lower layer of an overlayfs, so all writes are redirected
	// to the upper layer. Without write permission on lower-layer directories,
	// the kernel denies file creation before overlayfs can redirect the write.
	attr := fuse.Attr{
		Mode:  uint32(dir.UnixMode) | 0222,
		Owner: fuseOwner,
	}
	if !dir.Mtime.IsZero() {
		attr.Mtime = uint64(dir.Mtime.Unix())
		attr.Mtimensec = uint32(dir.Mtime.Nanosecond())
	}
	return attr
}

// casDir is a virtual directory node in the FUSE filesystem.
type casDir struct {
	fs.Inode
	attr fuse.Attr
}

var _ = (fs.NodeGetattrer)((*casDir)(nil))

func (d *casDir) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Attr = d.attr
	return 0
}

// casFile is a virtual file node backed by a CAS blob. File contents are
// served on demand by opening the CAS file and using splice for zero-copy
// reads.
type casFile struct {
	fs.Inode
	casPath string
	attr    fuse.Attr
}

var _ = (fs.NodeGetattrer)((*casFile)(nil))
var _ = (fs.NodeOpener)((*casFile)(nil))

func (f *casFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Attr = f.attr
	return 0
}

func (f *casFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	fd, err := syscall.Open(f.casPath, syscall.O_RDONLY, 0)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}
	// FOPEN_KEEP_CACHE tells the kernel to keep the page cache between opens.
	// CAS files are immutable, so this is always safe and avoids re-reading
	// files that are shared across actions.
	return &casFileHandle{fd: fd}, fuse.FOPEN_KEEP_CACHE, 0
}

// casFileHandle wraps a raw file descriptor for zero-copy reads from CAS.
type casFileHandle struct {
	fd int
}

var _ = (fs.FileReader)((*casFileHandle)(nil))
var _ = (fs.FileReleaser)((*casFileHandle)(nil))

func (fh *casFileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	return fuse.ReadResultFd(uintptr(fh.fd), off, len(dest)), 0
}

func (fh *casFileHandle) Release(ctx context.Context) syscall.Errno {
	syscall.Close(fh.fd)
	return 0
}

// casSymlink is a virtual symlink node.
type casSymlink struct {
	fs.Inode
	target string
}

var _ = (fs.NodeReadlinker)((*casSymlink)(nil))
var _ = (fs.NodeGetattrer)((*casSymlink)(nil))

func (s *casSymlink) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	return []byte(s.target), 0
}

func (s *casSymlink) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0777
	out.Owner = fuseOwner
	return 0
}

// MountCASFS mounts the CAS-backed FUSE filesystem at the given mountpoint.
// The returned server must be unmounted via server.Unmount() on shutdown.
func MountCASFS(mountpoint string) (*CASRoot, *fuse.Server, error) {
	root := &CASRoot{
		sandboxes: make(map[string]*fs.Inode),
	}

	// CAS files are immutable and the directory tree is static per-sandbox,
	// so we use very long cache timeouts to avoid unnecessary kernel→userspace
	// round-trips for re-validating entries and attributes.
	cacheTimeout := time.Hour
	opts := &fs.Options{
		AttrTimeout:     &cacheTimeout,
		EntryTimeout:    &cacheTimeout,
		NegativeTimeout: &cacheTimeout,
		MountOptions: fuse.MountOptions{
			FsName: "kajiya-cas",
			Name:   "kajiya",
			// DirectMount uses syscall.Mount instead of the fusermount
			// helper, avoiding the overhead of spawning a subprocess.
			DirectMount: true,
			// Symlinks in CAS are immutable, so the kernel can cache
			// Readlink results indefinitely.
			EnableSymlinkCaching: true,
			// The FUSE layer has no extended attributes. Returning ENOSYS
			// once tells the kernel to skip all future xattr queries,
			// which avoids round-trips from overlayfs checking for
			// user.overlay.opaque on lower-layer directories.
			DisableXAttrs: true,
			// Use 1 MiB buffers to reduce kernel-userspace round-trips
			// for large file reads.
			MaxWrite:     1 << 20,
			MaxReadAhead: 1 << 20,
			// Allow the kernel to queue more async I/O requests (readahead,
			// async reads) before blocking. The default of 12 is far too low
			// for parallel build actions.
			MaxBackground: 512,
		},
		NullPermissions: true,
	}

	server, err := fs.Mount(mountpoint, root, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("mount CAS FUSE filesystem: %w", err)
	}

	return root, server, nil
}
