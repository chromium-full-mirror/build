// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build linux

package localexec

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/model"
)

const pipeUserPagesSoftPath = "/proc/sys/fs/pipe-user-pages-soft"

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
	sandboxes map[string]*sandboxTree

	// fileInodes caches persistent inodes for CAS files. All paths that
	// serve a blob with the same attributes, in all sandboxes, share its
	// inode, because a regular file may have any number of names. They
	// share its page cache while the kernel holds a dentry for any of them.
	//
	// Directory inodes are never shared. RegisterSandbox creates one for
	// each directory of each input tree, even where two paths, in one tree
	// or in two, hold the same Directory. The kernel keeps a single dentry
	// per directory inode, so a lookup through another path moves it there
	// (d_splice_alias), or fails with ESTALE if it can't take the locks for
	// the move right away. And overlayfs hashes its inodes by their lower
	// inode, so it serves two paths of one sandbox with one inode: once one
	// of them gets an upper directory (a declared output's parent, or a
	// copy-up), lookups through the other fail with ESTALE.
	fileInodes sync.Map // map[fileKey]*fs.Inode
}

// fileKey identifies a shared file inode by the CAS path of its blob, which
// embeds the digest function's root, and by the attributes it reports.
type fileKey struct {
	casPath string
	attr    fuse.Attr
}

// sandboxTree is the inode tree that RegisterSandbox built for one sandbox.
type sandboxTree struct {
	root *fs.Inode

	// dirs lists every directory inode of the tree with the files linked
	// into it.
	dirs []sandboxDir
}

// sandboxDir is a directory inode of a sandboxTree and the files linked
// into it.
type sandboxDir struct {
	inode *fs.Inode
	files []model.KajiyaFile
}

// unlinkFiles removes the tree's links to its file inodes. go-fuse records
// every link in the linked inode, so a shared file inode would otherwise
// keep the directories of every sandbox that ever linked it alive.
func (t *sandboxTree) unlinkFiles() {
	for _, d := range t.dirs {
		for _, f := range d.files {
			d.inode.RmChild(f.Name)
		}
	}
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
func (r *CASRoot) RegisterSandbox(sandboxID string, trie *model.DirectoryTrie, fn digest.Function, cas *blobstore.ContentAddressableStorage, fuseMountpoint string, recorder *AccessRecorder) (string, error) {
	ctx := context.Background()
	now := time.Now()

	// Map from trie path to inode for building the tree.
	inodes := map[string]*fs.Inode{}

	tree := &sandboxTree{}
	var walkErr error

	trie.Root().Walk(func(k []byte, dir *model.KajiyaDirectory) bool {
		triePath := string(k) // e.g. "" for root, "src/" for a child.
		trieDir := strings.TrimSuffix(triePath, "/")

		if tree.root == nil {
			// The sandbox root is always per-sandbox (unique sandbox ID).
			tree.root = r.EmbeddedInode().NewPersistentInode(ctx, &casDir{
				attr: dirAttr(dir, now),
			}, fs.StableAttr{Mode: syscall.S_IFDIR})
			inodes[triePath] = tree.root
		}

		dirInode, ok := inodes[triePath]
		if !ok {
			walkErr = fmt.Errorf("parent inode not found for path %q", triePath)
			return true
		}
		tree.dirs = append(tree.dirs, sandboxDir{inode: dirInode, files: dir.Files})

		// Update attrs if the inode was pre-created by a parent's Dirs list
		// with default attributes.
		if dirOps, ok := dirInode.Operations().(*casDir); ok {
			dirOps.attr = dirAttr(dir, now)
		}

		// Add files. When tracing is disabled, reuse shared inodes so the
		// kernel's page cache is shared across actions. When tracing is
		// enabled, create per-sandbox inodes so each carries the recorder
		// and input-root-relative path.
		for _, f := range dir.Files {
			key := fileKey{
				casPath: cas.Path(fn, f.Digest),
				attr: fuse.Attr{
					Mode: uint32(f.UnixMode),
					Size: uint64(f.Digest.SizeBytes),
					// With nlink > 1, overlayfs gives each path its own inode.
					Nlink:     2,
					Owner:     fuseOwner,
					Mtime:     uint64(f.Mtime.Unix()),
					Mtimensec: uint32(f.Mtime.Nanosecond()),
				},
			}
			// Look up the shared inode before creating one, which takes
			// go-fuse's global lock.
			var fileInode *fs.Inode
			if recorder == nil {
				if cached, ok := r.fileInodes.Load(key); ok {
					fileInode = cached.(*fs.Inode)
				}
			}
			if fileInode == nil {
				cf := casFile{
					casPath:  key.casPath,
					recorder: recorder,
					attr:     key.attr,
				}
				// When tracing, record the file path relative to the input
				// root so we can report back which input files were accessed
				// by the action.
				if recorder != nil {
					cf.inputPath = path.Join(trieDir, f.Name)
				}
				fileInode = dirInode.NewPersistentInode(ctx, &cf, fs.StableAttr{Mode: syscall.S_IFREG})
				// When not tracing, the casFile has no per-sandbox state, so
				// cache it for reuse by future sandboxes. This lets the kernel
				// share page cache entries for the file across sandboxes. Use
				// LoadOrStore to avoid a TOCTOU race: if another goroutine
				// already cached an inode for this key, reuse it.
				if recorder == nil {
					cached, _ := r.fileInodes.LoadOrStore(key, fileInode)
					fileInode = cached.(*fs.Inode)
				}
			}
			dirInode.AddChild(f.Name, fileInode, true)
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
		for _, sd := range dir.Dirs {
			childInode := dirInode.NewPersistentInode(ctx, &casDir{}, fs.StableAttr{Mode: syscall.S_IFDIR})
			dirInode.AddChild(sd, childInode, true)
			inodes[path.Join(trieDir, sd)+"/"] = childInode
		}

		return false
	})

	if walkErr != nil {
		// Unlink the partial tree's files, as UnregisterSandbox would.
		tree.unlinkFiles()
		return "", walkErr
	}

	if tree.root == nil {
		return "", fmt.Errorf("empty input trie")
	}

	// Only hold the lock for the final attachment to the root inode and
	// map update. The subtree construction above is lock-free because each
	// sandbox operates on its own independent inode subtree.
	r.mu.Lock()
	r.EmbeddedInode().AddChild(sandboxID, tree.root, true)
	r.sandboxes[sandboxID] = tree
	r.mu.Unlock()

	return filepath.Join(fuseMountpoint, sandboxID), nil
}

// UnregisterSandbox removes the virtual directory tree for the given sandbox.
func (r *CASRoot) UnregisterSandbox(sandboxID string) {
	// Detach the sandbox's tree from the root.
	r.mu.Lock()
	tree, ok := r.sandboxes[sandboxID]
	if ok {
		r.EmbeddedInode().RmChild(sandboxID)
		delete(r.sandboxes, sandboxID)
	}
	r.mu.Unlock()
	if !ok {
		return
	}

	// Unlink the shared file inodes, which remain in fileInodes for reuse
	// by other sandboxes. The rest of the tree becomes garbage once the
	// kernel forgets the inodes it looked up.
	tree.unlinkFiles()

	// Drop the kernel's dentries for the tree, so that it forgets the tree's
	// inodes now instead of under memory pressure. The page cache of shared
	// files that no other dentry holds goes with them. ENOENT only means
	// that the kernel holds no dentry for the sandbox.
	if errno := r.EmbeddedInode().NotifyEntry(sandboxID); errno != 0 && errno != syscall.ENOENT {
		slog.Warn("failed to invalidate FUSE sandbox entry", "sandbox", sandboxID, "err", errno)
	}
}

// Close releases resources held by the CASRoot. Must be called before unmount.
func (r *CASRoot) Close() {
}

// dirAttr returns the attributes of a sandbox directory for dir. Without an
// mtime, it reports registered, the time its sandbox was registered, as a
// directory created for the sandbox would.
func dirAttr(dir *model.KajiyaDirectory, registered time.Time) fuse.Attr {
	// Always include write permission for all users. The FUSE filesystem
	// serves as the lower layer of an overlayfs, so all writes are redirected
	// to the upper layer. Without write permission on lower-layer directories,
	// the kernel denies file creation before overlayfs can redirect the write.
	attr := fuse.Attr{
		Mode:  uint32(dir.UnixMode) | 0222,
		Owner: fuseOwner,
	}
	mtime := dir.Mtime
	if mtime.IsZero() {
		mtime = registered
	}
	attr.Mtime = uint64(mtime.Unix())
	attr.Mtimensec = uint32(mtime.Nanosecond())
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
	// When non-nil, Open calls are recorded for input tracing.
	recorder  *AccessRecorder
	inputPath string // path relative to input root, e.g. "src/hello.cc"
}

var _ = (fs.NodeGetattrer)((*casFile)(nil))
var _ = (fs.NodeOpener)((*casFile)(nil))

// Getattr reports the file's attributes, with the CAS blob's mtime for an
// input without one, as a hard link to the blob would show.
func (f *casFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Attr = f.attr
	// RegisterSandbox stores the zero time for an input without an mtime.
	if int64(f.attr.Mtime) == (time.Time{}).Unix() {
		var st syscall.Stat_t
		if err := syscall.Stat(f.casPath, &st); err != nil {
			return fs.ToErrno(err)
		}
		out.Mtime = uint64(st.Mtim.Sec)
		out.Mtimensec = uint32(st.Mtim.Nsec)
	}
	return 0
}

func (f *casFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if f.recorder != nil {
		f.recorder.Record(f.inputPath)
	}
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

func pipeUserPagesSoftLimit(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	limit, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, err
	}
	return limit, nil
}

func disableSpliceForPipeSoftLimit(path string) bool {
	limit, err := pipeUserPagesSoftLimit(path)
	if err != nil {
		slog.Warn("disabling FUSE splice because pipe-user-pages-soft could not be read",
			"path", path,
			"error", err)
		return true
	}

	if limit == 0 {
		return false
	}

	slog.Warn("disabling FUSE splice because pipe-user-pages-soft is set",
		"path", path,
		"limit_pages", limit,
		"hint", "set fs.pipe-user-pages-soft=0 to keep FUSE splice enabled")
	return true
}

// MountCASFS mounts the CAS-backed FUSE filesystem at the given mountpoint.
// The returned server must be unmounted via server.Unmount() on shutdown.
func MountCASFS(mountpoint string) (*CASRoot, *fuse.Server, error) {
	root := &CASRoot{
		sandboxes: make(map[string]*sandboxTree),
	}
	disableSplice := disableSpliceForPipeSoftLimit(pipeUserPagesSoftPath)

	// CAS files are immutable and the directory tree is static per-sandbox,
	// so we use very long cache timeouts to avoid unnecessary kernel→userspace
	// round-trips for re-validating entries and attributes.
	cacheTimeout := time.Hour
	opts := &fs.Options{
		AttrTimeout:     &cacheTimeout,
		EntryTimeout:    &cacheTimeout,
		NegativeTimeout: &cacheTimeout,
		FsName:          "kajiya-cas",
		Name:            "kajiya",
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
		// Conditionally disable the splice fast path based on the
		// pipe-user-pages-soft limit. When set to "unlimited", the
		// splice fast path can be used safely, otherwise it is
		// disabled to prevent F_SETPIPE_SZ failures under heavy
		// parallel reads.
		DisableSplice: disableSplice,
		// Use 1 MiB buffers to reduce kernel-userspace round-trips
		// for large file reads.
		MaxWrite:     1 << 20,
		MaxReadAhead: 1 << 20,
		// Allow the kernel to queue more async I/O requests (readahead,
		// async reads) before blocking. The default of 12 is far too low
		// for parallel build actions.
		MaxBackground:   512,
		NullPermissions: true,
	}

	server, err := fs.Mount(mountpoint, root, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("mount CAS FUSE filesystem: %w", err)
	}

	return root, server, nil
}
