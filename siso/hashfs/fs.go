// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package hashfs provides a filesystem with digest hash.
package hashfs

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/golang/glog"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.chromium.org/build/siso/hashfs/osfs"
	pb "go.chromium.org/build/siso/hashfs/proto"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/o11y/trace"
	"go.chromium.org/build/siso/reapi/digest"
	"go.chromium.org/build/siso/reapi/merkletree"
	"go.chromium.org/build/siso/runtimex"
	"go.chromium.org/build/siso/sync/semaphore"
)

// Linux imposes a limit of at most 40 symlinks in any one path lookup.
// see: https://lwn.net/Articles/650786/
const maxSymlinks = 40

// ForgetMissingsSemaphore is a semaphore to control concurrent ForgetMissings.
// os.Lstat in ForgetMissings would create lots of thread. b/325565625
var ForgetMissingsSemaphore = semaphore.New("fs-forget", runtimex.NumCPU()*2)

// FlushSemaphore is a semaphore to control concurrent flushes.
var FlushSemaphore = semaphore.New("fs-flush", runtimex.NumCPU()*8)

func isExecutable(fi fs.FileInfo, fname string, m map[string]bool) bool {
	if fi.Mode()&0111 != 0 {
		return true
	}
	return m[fname]
}

// NotifyFunc is the type of the function to notify the filesystem changes.
type NotifyFunc func(context.Context, *FileInfo)

// HashFS is a filesystem for digest hash.
type HashFS struct {
	opt       Option
	directory *directory

	notifies []NotifyFunc

	// OS wraps of OS I/O operations in the HashFS.
	OS *osfs.OSFS

	digester digester

	// loadErr keeps load error.
	loadErr error

	// clean if loaded state is matched with local disk's state.
	clean atomic.Bool

	// buildTargets is build targets stored in state file.
	// nil vs []string{} differs.
	// nil is not set (last build failed).
	// []string{} is set (last build succeeded without explicit target requested).
	buildTargets []string

	// loaded if state is loaded.
	loaded atomic.Bool

	// holds generated files (full path) in previous builds.
	previouslyGeneratedFiles []string

	// holds tainted files
	taintedFiles []string

	executables map[string]bool

	// writer for updated entries journal.
	journal journalWriter

	// trigger for SetState background goroutine finish.
	setStateCh chan error

	// records missing outputs to make hashfs non-clean when missing
	// outputs exists, so trigger rebuilds b/374179435
	missingOutputs sync.Map
}

// New creates a HashFS.
func New(ctx context.Context, opt Option) (*HashFS, error) {
	defer trace.Begin(ctx, "hashfs.New").End()
	if opt.OutputLocal == nil {
		opt.OutputLocal = func(context.Context, string) bool { return false }
	}
	if opt.Ignore == nil {
		opt.Ignore = func(context.Context, string) bool { return false }
	}
	if opt.DataSource == nil {
		opt.DataSource = noDataSource{}
	}
	fsys := &HashFS{
		opt:       opt,
		directory: &directory{isRoot: true},
		OS:        osfs.New(ctx, "fs", opt.OSFSOption),

		digester: digester{
			quitEarly: opt.DeferDigest,
			q:         make(chan digestReq, 1000),
			quit:      make(chan struct{}),
			done:      make(chan struct{}),
		},
	}
	if opt.StateFile != "" {
		start := time.Now()
		journalFile := opt.StateFile + ".journal"

		fstate, err := Load(ctx, opt)
		if errors.Is(err, fs.ErrNotExist) {
			clog.Infof(ctx, "missing fs state. new build? %v", err)
			// missing .siso_fs_state is not considered as load error.
			fstate = &pb.State{}
		} else if err != nil {
			clog.Warningf(ctx, "Failed to load fs state from %s: %v", opt.StateFile, err)
			fsys.loadErr = err
			fstate = &pb.State{}
		} else {
			clog.Infof(ctx, "Load fs state from %s: %s", opt.StateFile, time.Since(start))
		}

		// Recover last build updates from the journal if the previous
		// build didn't finish properly (journal file not removed).
		// Skip the journal when state was corrupted, since we don't
		// have a valid base state to apply it to.
		var reconciled bool
		if fsys.loadErr == nil {
			reconciled = loadJournal(ctx, journalFile, fstate)
		}
		if err := fsys.SetState(ctx, fstate); err != nil {
			return nil, err
		}
		if reconciled {
			// Save fstate to make it base state for next journaling.
			err := Save(ctx, fstate, opt)
			if err != nil {
				clog.Errorf(ctx, "Failed to save reconciled fs state in %s: %v", opt.StateFile, err)
			}
		}
		err = os.Remove(journalFile)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			clog.Warningf(ctx, "Failed to remove journal: %v", err)
		}

		f, err := os.Create(journalFile)
		if err != nil {
			clog.Warningf(ctx, "Failed to create fs state journal: %v", err)
		} else {
			fsys.journal.w = f
		}
	}
	go fsys.digester.start(ctx)
	return fsys, nil
}

// LoadErr returns load error.
func (hfs *HashFS) LoadErr() error {
	return hfs.loadErr
}

// WaitReady waits fs state is updated in memory.
func (hfs *HashFS) WaitReady(ctx context.Context) error {
	if hfs.setStateCh == nil {
		return nil
	}
	started := time.Now()
	select {
	case <-ctx.Done():
		clog.Warningf(ctx, "hashfs does not become ready %s: %v", time.Since(started), context.Cause(ctx))
		return context.Cause(ctx)

	case err := <-hfs.setStateCh:
		hfs.setStateCh = nil
		if err != nil {
			clog.Errorf(ctx, "hashfs does not become ready %s: %v", time.Since(started), err)
			return err
		}
		clog.Infof(ctx, "hashfs becomes ready: %v", time.Since(started))
	}
	return nil
}

// Notify causes the hashfs to relay filesystem motification to f.
func (hfs *HashFS) Notify(f NotifyFunc) {
	hfs.notifies = append(hfs.notifies, f)
}

// SetExecutables sets a map of full paths for files to be
// considered as executable, even if it is not executable on local disk.
func (hfs *HashFS) SetExecutables(ctx context.Context, m map[string]bool) {
	hfs.executables = m
	for fname := range m {
		e, _, _, ok := hfs.directory.lookup(ctx, fname)
		if ok {
			clog.Infof(ctx, "set executable bit on %q", fname)
			e.mode |= 0111
		}
	}
}

// SetBuildTargets sets build targets.
func (hfs *HashFS) SetBuildTargets(ctx context.Context, buildTargets []string, success bool) {
	if !success {
		hfs.buildTargets = nil
		clog.Infof(ctx, "set no build targets")
		return
	}
	hfs.buildTargets = make([]string, len(buildTargets))
	copy(hfs.buildTargets, buildTargets)
	clog.Infof(ctx, "set build targets=%q", hfs.buildTargets)
}

// shouldSkipSave reports whether the current state should not be
// persisted to disk. Each condition is a reason the in-memory state
// is unreliable or unchanged.
func (hfs *HashFS) shouldSkipSave() bool {
	// State matches disk, nothing to write.
	if hfs.clean.Load() {
		return true
	}
	// State was never fully loaded, saving would lose data.
	if !hfs.loaded.Load() {
		return true
	}
	// Deferred-digest mode with no journal updates means no changes.
	if hfs.opt.DeferDigest && hfs.journal.n == 0 {
		return true
	}
	// Tainted files present: state may be corrupted.
	if len(hfs.taintedFiles) > 0 {
		return true
	}
	return false
}

// Close closes the HashFS.
// Persists current state in opt.StateFile.
func (hfs *HashFS) Close(ctx context.Context) error {
	clog.Infof(ctx, "fs close")
	hfs.digester.stop(ctx)
	if hfs.opt.StateFile == "" {
		return nil
	}
	err := hfs.journal.Close()
	if err != nil {
		clog.Warningf(ctx, "Failed to close journal %v", err)
	}
	clog.Infof(ctx, "close journal")
	if hfs.shouldSkipSave() {
		clog.Warningf(ctx, "not save state clean=%t loaded=%t journal:%d tainted:%d", hfs.clean.Load(), hfs.loaded.Load(), hfs.journal.n, len(hfs.taintedFiles))
		return nil
	}
	err = Save(ctx, hfs.State(ctx), hfs.opt)
	if err != nil {
		clog.Errorf(ctx, "Failed to save fs state in %s: %v", hfs.opt.StateFile, err)
		if rerr := os.Remove(hfs.opt.StateFile); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			clog.Errorf(ctx, "Failed to remove stale fs state %s: %v", hfs.opt.StateFile, err)
		}
		return err
	}
	clog.Infof(ctx, "Saved fs state in %s", hfs.opt.StateFile)
	return nil
}

// IsClean returns whether hashfs is clean for buildTargets (i.e. sync with local disk).
func (hfs *HashFS) IsClean(buildTargets []string) bool {
	if !hfs.clean.Load() {
		return false
	}
	// we distinguish nil vs []string{}.
	if hfs.buildTargets == nil {
		return false
	}
	return slices.Equal(hfs.buildTargets, buildTargets)
}

// PreviouslyGeneratedFiles returns a list of generated files
// (i.e. has cmdhash) in the previous builds.
// It will reset internal data, so next call will return nil
func (hfs *HashFS) PreviouslyGeneratedFiles() []string {
	p := hfs.previouslyGeneratedFiles
	hfs.previouslyGeneratedFiles = nil
	return p
}

// TaintedFiles returns a list of manually modified generated files.
func (hfs *HashFS) TaintedFiles() []string {
	return hfs.taintedFiles
}

// AddMissingOutput adds a missing output.
func (hfs *HashFS) AddMissingOutput(ctx context.Context, root, fname string) {
	hfs.missingOutputs.Store(filepath.ToSlash(filepath.Join(root, fname)), true)
}

// FileSystem returns FileSystem interface at dir.
func (hfs *HashFS) FileSystem(ctx context.Context, dir string) FileSystem {
	return FileSystem{
		hashFS: hfs,
		ctx:    ctx,
		dir:    dir,
	}
}

// DataSource returns DataSource of the HashFS.
func (hfs *HashFS) DataSource() DataSource {
	return hfs.opt.DataSource
}

// OnCog returns whether it is on Cog or not.
func (hfs *HashFS) OnCog() bool {
	return hfs.opt.CogFS != nil
}

func needPathClean(names ...string) bool {
	for _, name := range names {
		// even on windows, we use /-path in hashfs.
		if strings.Contains(name, `\`) {
			return true
		}
		if strings.Contains(name, "//") {
			return true
		}
		i := strings.IndexByte(name, '.')
		if i < 0 {
			continue
		}
		name = name[i:]
		if strings.HasPrefix(name, "./") || strings.HasPrefix(name, "../") {
			return true
		}
	}
	return false
}

func makeFullpath(root, fname string) string {
	if filepath.IsAbs(fname) {
		return filepath.ToSlash(fname)
	}
	return filepath.ToSlash(filepath.Join(root, fname))
}

func (hfs *HashFS) dirLookup(ctx context.Context, root, fname string) (*entry, string, *directory, bool) {
	if filepath.IsAbs(fname) {
		return hfs.directory.lookup(ctx, filepath.ToSlash(fname))
	}
	if needPathClean(root, fname) {
		return hfs.directory.lookup(ctx, filepath.ToSlash(filepath.Join(root, fname)))
	}
	e, _, _, ok := hfs.directory.lookup(ctx, root)
	if !ok {
		return nil, fname, nil, false
	}
	if e.directory == nil {
		return nil, fname, nil, false
	}
	e, dir, resolved, ok := e.directory.lookupEntry(ctx, fname)
	if ok {
		return e, fname, dir, true
	}
	if resolved != "" {
		resolvedName := resolved
		if !filepath.IsAbs(resolved) {
			resolvedName = filepath.ToSlash(filepath.Join(root, resolved))
		}
		return hfs.directory.lookup(ctx, resolvedName)
	}
	return nil, fname, nil, false
}

func (hfs *HashFS) dirStoreAndNotify(ctx context.Context, fullname string, e *entry) error {
	ee, err := hfs.directory.store(ctx, fullname, e)
	if err != nil {
		return err
	}
	hfs.digester.lazyCompute(ctx, fullname, ee)
	for _, f := range hfs.notifies {
		f(ctx, &FileInfo{fname: fullname, e: ee})
	}
	return nil
}

// getOrCreateEntry looks up fname in the directory tree, creating and
// storing a new local entry from disk if not found.
// Returns the entry and the resolved fname (symlinks in intermediate
// path components followed).
func (hfs *HashFS) getOrCreateEntry(ctx context.Context, fname string) (*entry, string, error) {
	e, fname, _, ok := hfs.directory.lookup(ctx, fname)
	if ok {
		e.mu.Lock()
		err := e.err
		e.mu.Unlock()
		if err != nil {
			return nil, fname, err
		}
		return e, fname, nil
	}
	e = newLocalEntry()
	e.init(ctx, fname, hfs.executables, hfs.OS)
	if errors.Is(e.err, context.Canceled) {
		return nil, fname, e.err
	}
	e, err := hfs.directory.store(ctx, fname, e)
	if err != nil {
		clog.Warningf(ctx, "failed to store %s %s: %v", fname, e, err)
		return nil, fname, err
	}
	if e.err != nil {
		return nil, fname, e.err
	}
	clog.Infof(ctx, "stat new entry %s %s", fname, e)
	return e, fname, nil
}

// Stat returns a FileInfo at root/fname.
func (hfs *HashFS) Stat(ctx context.Context, root, fname string) (FileInfo, error) {
	return hfs.stat(ctx, root, fname, true)
}

func (hfs *HashFS) stat(ctx context.Context, root, fname string, needCompute bool) (FileInfo, error) {
	if log.V(1) {
		clog.Infof(ctx, "stat @%s %s", root, fname)
	}
	e, fname, dir, ok := hfs.dirLookup(ctx, root, fname)
	if log.V(1) {
		clog.Infof(ctx, "stat @%s -> %s", root, fname)
	}
	if ok {
		e.mu.Lock()
		err := e.err
		e.mu.Unlock()
		if err != nil {
			return FileInfo{}, err
		}
		if e.directory != nil {
			// directory's mtime has been updated locally
			// where hashfs doesn't know. e.g. add new file
			// in the directory by local run.
			fullname := makeFullpath(root, fname)
			lfi, err := hfs.OS.Lstat(ctx, fullname)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				// virtually created dir in hashfs,
				// so no need to update mtime.
				clog.Infof(ctx, "stat hashfs dir %s. doesn't exist in local", fullname)
				return FileInfo{}, err
			case err != nil:
				clog.Warningf(ctx, "unexpected dir stat fail %s: %v", fullname, err)
				return FileInfo{}, err
			default:
				mtime := lfi.ModTime()
				// adjust for clock stepback by NTP
				err = waitUntilModTime(ctx, fullname, mtime)
				if err != nil {
					return FileInfo{}, err
				}
				e.mu.Lock()
				e.mtime = mtime
				if e.updatedTime.Before(mtime) {
					// if no cmdhash, it may not be generated by any step, so keep updated_time with mtime silently.
					if len(e.cmdhash) > 0 {
						clog.Warningf(ctx, "unexpected update dir mtime %s %v; updated_time=%v", fullname, mtime, e.updatedTime)
					}
					e.updatedTime = mtime
				}
				e.mu.Unlock()
			}
		}
		return FileInfo{root: root, fname: fname, e: e}, nil
	}
	fullname := makeFullpath(root, fname)
	e = newLocalEntry()
	e.init(ctx, fullname, hfs.executables, hfs.OS)
	if log.V(1) {
		clog.Infof(ctx, "stat new entry %s %s", fullname, e)
	}
	if errors.Is(e.err, context.Canceled) {
		return FileInfo{}, e.err
	}
	var err error
	if dir != nil {
		e, err = dir.store(ctx, filepath.Base(fullname), e)
		if errors.Is(err, errRootSymlink) {
			e, err = hfs.directory.store(ctx, fullname, e)
		}
	} else {
		e, err = hfs.directory.store(ctx, fullname, e)
	}
	if err != nil {
		clog.Warningf(ctx, "failed to store %s %s in %s: %v", fullname, e, dir, err)
		return FileInfo{}, err
	}
	if e.err != nil {
		return FileInfo{}, e.err
	}
	if needCompute {
		hfs.digester.lazyCompute(ctx, fullname, e)
	}
	return FileInfo{root: root, fname: fname, e: e}, nil
}

// SymlinkError is an error when reading symlink file.
type SymlinkError struct {
	Path   string
	Target string
}

func (e SymlinkError) Error() string {
	return fmt.Sprintf("reading symlink %q: target=%q", e.Path, e.Target)
}

// ReadDir returns directory entries of root/name.
func (hfs *HashFS) ReadDir(ctx context.Context, root, name string) (dents []DirEntry, err error) {
	ctx, span := trace.NewSpan(ctx, "read-dir")
	defer span.Close(nil)
	if log.V(1) {
		clog.Infof(ctx, "readdir @%s %s", root, name)
		defer func() {
			clog.Infof(ctx, "readdir @%s %s -> %d %v", root, name, len(dents), err)
		}()
	}
	dname := makeFullpath(root, name)
	e, dname, err := hfs.getOrCreateEntry(ctx, dname)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", dname, err)
	}
	if e.isSymlink() {
		relDname, err := filepath.Rel(root, dname)
		if err != nil || !filepath.IsLocal(relDname) {
			clog.Warningf(ctx, "read dir: symlink rel root %q: %v", dname, err)
			return nil, SymlinkError{Path: dname, Target: e.target}
		}
		return nil, SymlinkError{Path: relDname, Target: e.target}
	}
	if e.directory == nil {
		return nil, fmt.Errorf("read dir %s: not dir: %w", dname, os.ErrPermission)
	}
	// TODO(ukai): fix race in updateDir -> store.
	names := e.updateDir(ctx, hfs, dname)
	if log.V(1) {
		clog.Infof(ctx, "update-dir %s -> %d", dname, len(names))
	}
	var ents []DirEntry
	e.directory.m.Range(func(k, v any) bool {
		name := k.(string)
		ee := v.(*entry)
		if ee.err != nil {
			return true
		}
		ents = append(ents, DirEntry{
			fi: FileInfo{
				root:  root,
				fname: filepath.ToSlash(filepath.Join(dname, name)),
				e:     ee,
			},
		})
		return true
	})
	return ents, nil
}

// ReadFile reads a contents of root/fname.
func (hfs *HashFS) ReadFile(ctx context.Context, root, fname string) ([]byte, error) {
	ctx, span := trace.NewSpan(ctx, "read-file")
	defer span.Close(nil)
	if log.V(1) {
		clog.Infof(ctx, "readfile @%s %s", root, fname)
	}
	fname = makeFullpath(root, fname)
	span.SetAttr("fname", fname)
	e, fname, err := hfs.getOrCreateEntry(ctx, fname)
	if err != nil {
		return nil, fmt.Errorf("read file %s: %w", fname, err)
	}
	if len(e.buf) > 0 {
		return e.buf, nil
	}
	e.mu.RLock()
	ed := e.d
	e.mu.RUnlock()
	// If digest is known and the file is not flushed to disk, read from CAS.
	// Otherwise, read from local disk, which will also compute the digest if unknown.
	if !ed.IsZero() {
		// digest is known.
		lfi, err := hfs.OS.Lstat(ctx, fname)
		// check it is already flushed to disk or not.
		if err != nil || !e.getMtime().Equal(lfi.ModTime()) || ed.SizeBytes != lfi.Size() {
			// not yet flushed, read from CAS
			buf, err := digest.DataToBytes(ctx, digest.NewData(e.src, ed))
			if log.V(1) {
				clog.Infof(ctx, "readfile(%s) %s: %v", ed, fname, err)
			}
			return buf, err
		}
		// already flushed. reading from local disk is faster.
	}
	if e.isSymlink() {
		relFname, err := filepath.Rel(root, fname)
		if err != nil || !filepath.IsLocal(relFname) {
			clog.Warningf(ctx, "readfile: symlink rel root %q: %v", fname, err)
			return nil, SymlinkError{Path: fname, Target: e.target}
		}
		return nil, SymlinkError{Path: relFname, Target: e.target}
	}
	if e.src == nil {
		return nil, fmt.Errorf("readfile %s: no src", fname)
	}
	src := hfs.OS.FileSource(fname, -1)
	rd, err := src.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("readfile %s: %w", fname, err)
	}
	defer rd.Close()
	size := max(e.size, 0)
	buf := make([]byte, size)
	_, err = io.ReadFull(rd, buf)
	if log.V(1) {
		clog.Infof(ctx, "readfile(disk) %s: %v", fname, err)
	}
	// async compute digest.
	hfs.digester.lazyCompute(ctx, fname, e)
	return buf, err
}

// WriteFile writes a contents in root/fname with mtime and cmdhash, edgehash.
func (hfs *HashFS) WriteFile(ctx context.Context, root, fname string, b []byte, isExecutable bool, mtime time.Time, cmdhash, edgehash []byte) error {
	ctx, span := trace.NewSpan(ctx, "write-file")
	defer span.Close(nil)
	if log.V(1) {
		clog.Infof(ctx, "writefile @%s %s x:%t mtime:%s", root, fname, isExecutable, mtime)
	}
	hfs.clean.Store(false)
	data := digest.FromBytes(fname, b)
	fname = makeFullpath(root, fname)
	span.SetAttr("fname", fname)
	lready := make(chan bool, 1)
	lready <- true
	mode := fs.FileMode(0644)
	if isExecutable {
		mode |= 0111
	}
	e := &entry{
		lready:      lready,
		size:        data.Digest().SizeBytes,
		mtime:       mtime,
		mode:        mode,
		src:         data,
		d:           data.Digest(),
		buf:         b,
		cmdhash:     cmdhash,
		edgehash:    edgehash,
		updatedTime: time.Now(),
		isChanged:   true,
	}
	err := hfs.dirStoreAndNotify(ctx, fname, e)
	clog.Infof(ctx, "writefile %s x:%t mtime:%s: %v", fname, isExecutable, mtime, err)
	if err != nil {
		return err
	}
	hfs.journalEntry(ctx, fname, e)
	return nil
}

// Symlink creates a symlink to target at root/linkpath with mtime and cmdhash, edgehash.
func (hfs *HashFS) Symlink(ctx context.Context, root, target, linkpath string, mtime time.Time, cmdhash, edgehash []byte) error {
	if log.V(1) {
		clog.Infof(ctx, "symlink @%s %s -> %s", root, linkpath, target)
	}
	hfs.clean.Store(false)
	linkfname := makeFullpath(root, linkpath)
	lready := make(chan bool, 1)
	lready <- true
	e := &entry{
		lready:      lready,
		mtime:       mtime,
		mode:        0644 | fs.ModeSymlink,
		cmdhash:     cmdhash,
		edgehash:    edgehash,
		target:      target,
		updatedTime: time.Now(),
		isChanged:   true,
	}
	err := hfs.dirStoreAndNotify(ctx, linkfname, e)
	clog.Infof(ctx, "symlink @%s %s -> %s: %v", root, linkpath, target, err)
	if err != nil {
		return err
	}
	hfs.journalEntry(ctx, linkfname, e)
	return nil
}

// Copy copies a file from root/src to root/dst with mtime and cmdhash, edgehash.
// if src is dir, returns error.
func (hfs *HashFS) Copy(ctx context.Context, root, src, dst string, mtime time.Time, cmdhash, edgehash []byte) error {
	if log.V(1) {
		clog.Infof(ctx, "copy @%s %s to %s", root, src, dst)
	}
	hfs.clean.Store(false)
	srcfname := makeFullpath(root, src)
	dstfname := makeFullpath(root, dst)
	e, _, err := hfs.getOrCreateEntry(ctx, srcfname)
	if err != nil {
		return err
	}
	subdir := e.getDir()
	if subdir != nil {
		return fmt.Errorf("is a directory: %s", srcfname)
	}
	if lsrc, ok := e.src.(osfs.FileSource); ok {
		_, err := hfs.OS.Lstat(ctx, lsrc.Fname)
		if err != nil {
			// src file uses local file, but not available
			return fmt.Errorf("copy src: %w", err)
		}
	}
	if !e.isSymlink() {
		hfs.digester.compute(ctx, srcfname, e)
	}
	lready := make(chan bool, 1)
	lready <- true
	newEnt := &entry{
		lready:   lready,
		size:     e.size,
		mtime:    mtime,
		mode:     e.mode,
		cmdhash:  cmdhash,
		edgehash: edgehash,
		target:   e.target,
		// use the same data source as src if any.
		src:         e.src,
		d:           e.d,
		buf:         e.buf,
		updatedTime: time.Now(),
		isChanged:   true,
	}
	err = hfs.dirStoreAndNotify(ctx, dstfname, newEnt)
	if err != nil {
		return err
	}
	hfs.journalEntry(ctx, dstfname, newEnt)
	clog.Infof(ctx, "copy %s to %s", srcfname, dstfname)
	return nil
}

// Mkdir makes a directory at root/dirname.
func (hfs *HashFS) Mkdir(ctx context.Context, root, dirname string, cmdhash, edgehash []byte) error {
	if log.V(1) {
		clog.Infof(ctx, "mkdir @%s %s", root, dirname)
	}
	hfs.clean.Store(false)
	dirname = makeFullpath(root, dirname)
	fi, err := hfs.OS.Lstat(ctx, dirname)
	mtime := time.Now()
	if err == nil && fi.IsDir() {
		err := hfs.OS.Chtimes(ctx, dirname, time.Time{}, mtime)
		if err != nil {
			clog.Warningf(ctx, "failed to set dir mtime %s: %v: %v", dirname, mtime, err)
		}
	} else {
		err := hfs.OS.MkdirAll(ctx, dirname, 0755)
		if err != nil {
			return err
		}
		fi, err := hfs.OS.Lstat(ctx, dirname)
		if err != nil {
			return err
		}
		if mtime.Before(fi.ModTime()) {
			mtime = fi.ModTime()
		}
	}
	lready := make(chan bool, 1)
	lready <- true

	e := &entry{
		lready:      lready,
		mtime:       mtime,
		mode:        0644 | fs.ModeDir,
		cmdhash:     cmdhash,
		edgehash:    edgehash,
		directory:   &directory{},
		updatedTime: time.Now(),
		isChanged:   true,
	}
	err = hfs.dirStoreAndNotify(ctx, dirname, e)
	if serr, ok := errors.AsType[storeRaceError](err); ok {
		curEntry, ok := serr.curEntry.(*entry)
		if ok {
			// Mkdir succeeds if cur entry is the directory and has the same cmdhash, or cur cmdhash exists but trying to add no cmdhash.
			if curEntry != nil && curEntry.getDir() != nil && (bytes.Equal(cmdhash, curEntry.cmdhash) || (len(curEntry.cmdhash) > 0 && len(cmdhash) == 0)) {
				err = nil
			}
		}
	}
	clog.Infof(ctx, "mkdir %s %s: %v", dirname, mtime, err)
	if err != nil {
		return err
	}
	if len(cmdhash) > 0 {
		hfs.journalEntry(ctx, dirname, e)
	}
	return nil
}

// Remove removes a file at root/fname.
func (hfs *HashFS) Remove(ctx context.Context, root, fname string) error {
	if log.V(1) {
		clog.Infof(ctx, "remove @%s %s", root, fname)
	}
	hfs.clean.Store(false)
	fname = makeFullpath(root, fname)
	lready := make(chan bool, 1)
	lready <- true
	e := &entry{
		lready: lready,
		err:    fs.ErrNotExist,
	}
	_, err := hfs.directory.store(ctx, fname, e)
	clog.Infof(ctx, "remove %s: %v", fname, err)
	return err
}

// RemoveAll removes all files under root/name.
// Also removes from the disk at the same time.
func (hfs *HashFS) RemoveAll(ctx context.Context, root, name string) error {
	if log.V(1) {
		clog.Infof(ctx, "removeAll @%s %s", root, name)
	}
	hfs.clean.Store(false)
	name = makeFullpath(root, name)
	err := os.RemoveAll(name)
	if err == nil {
		err = fs.ErrNotExist
	}
	lready := make(chan bool, 1)
	lready <- true
	e := &entry{
		lready: lready,
		err:    err,
	}
	_, err = hfs.directory.store(ctx, name, e)
	e.mu.Lock()
	eErr := e.err
	e.mu.Unlock()
	clog.Infof(ctx, "removeAll %s [%v]: %v", name, eErr, err)
	return err
}

// Forget forgets cached entry for inputs under root.
func (hfs *HashFS) Forget(ctx context.Context, root string, inputs []string) {
	for _, fname := range inputs {
		fullname := makeFullpath(root, fname)
		hfs.directory.delete(ctx, fullname)
	}
}

// ForgetMissingsInDir forgets cached entry under root/dir if it isn't
// generated files/dirs by any steps and doesn't exist on local disk.
// It is used for a step that removes files under a dir. b/350662100
func (hfs *HashFS) ForgetMissingsInDir(ctx context.Context, root, dir string) {
	inputs := []string{dir}
	var needCheck []string
	for len(inputs) > 0 {
		fname := inputs[0]
		copy(inputs, inputs[1:])
		inputs = inputs[:len(inputs)-1]
		fi, err := hfs.Stat(ctx, root, fname)
		if errors.Is(err, fs.ErrNotExist) {
			// If it doesn't exist in hashfs,
			// no need to check more.
			continue
		}
		if err == nil {
			if fi.IsDir() {
				dents, err := hfs.ReadDir(ctx, root, fname)
				if err != nil {
					clog.Warningf(ctx, "readdir failed for %q: %v", fname, err)
					needCheck = append(needCheck, fname)
					continue
				}
				for _, dent := range dents {
					inputs = append(inputs, filepath.ToSlash(filepath.Join(fname, dent.Name())))
				}
			}
			if fi.IsChanged() {
				// it is explicitly generated file/dir,
				// no need to check more.
				continue
			}
		}
		needCheck = append(needCheck, fname)
	}
	err := ForgetMissingsSemaphore.Do(ctx, func(ctx context.Context) error {
		for _, fname := range needCheck {
			fullname := makeFullpath(root, fname)
			_, err := hfs.OS.Lstat(ctx, fullname)
			if errors.Is(err, fs.ErrNotExist) {
				clog.Infof(ctx, "forget missing %s", fullname)
				hfs.directory.delete(ctx, fullname)
				continue
			}
		}
		return nil
	})
	if err != nil {
		clog.Warningf(ctx, "forget missings in dir: %v", err)
	}
}

// ForgetMissings forgets cached entry for input under root
// if it doesn't exist on local disk, and returns valid inputs.
// It is currently used for deps=msvc only to workaround clang-cl issue.
// https://github.com/llvm/llvm-project/issues/58726
func (hfs *HashFS) ForgetMissings(ctx context.Context, root string, inputs []string) []string {
	availables := make([]string, 0, len(inputs))
	needCheck := make([]string, 0, len(inputs))
	for _, fname := range inputs {
		fi, err := hfs.Stat(ctx, root, fname)
		if errors.Is(err, fs.ErrNotExist) {
			// If it doesn't exist in hashfs,
			// no need to check with os.Lstat.
			clog.Infof(ctx, "remove from inputs %s: %v", fname, err)
			continue
		}
		if err == nil && (fi.IsChanged() || fi.IsMissingChecked()) {
			// it is explicit generated file.
			// no need to check on disk.
			availables = append(availables, fname)
			continue
		}
		needCheck = append(needCheck, fname)
	}

	err := ForgetMissingsSemaphore.Do(ctx, func(ctx context.Context) error {
		for _, fname := range needCheck {
			fullname := makeFullpath(root, fname)
			_, err := hfs.OS.Lstat(ctx, fullname)
			if errors.Is(err, fs.ErrNotExist) {
				clog.Infof(ctx, "forget missing %s", fullname)
				hfs.directory.delete(ctx, fullname)
				continue
			}
			fi, err := hfs.Stat(ctx, root, fname)
			if err == nil {
				fi.e.mu.Lock()
				fi.e.isMissingChecked = true
				fi.e.mu.Unlock()
			}
			availables = append(availables, fname)
		}
		return nil
	})
	if err != nil {
		clog.Warningf(ctx, "forget missings: %v", err)
	}
	return availables
}

// Availables returns valid inputs (i.e. exist in hashfs).
func (hfs *HashFS) Availables(ctx context.Context, root string, inputs []string) []string {
	availables := make([]string, 0, len(inputs))
	for _, fname := range inputs {
		_, err := hfs.Stat(ctx, root, fname)
		if errors.Is(err, fs.ErrNotExist) {
			// If it doesn't exist in hashfs,
			// no need to check with os.Lstat.
			clog.Infof(ctx, "remove from inputs %s: %v", fname, err)
			continue
		}
		availables = append(availables, fname)
	}
	return availables
}

// escapesRoot reports whether path lies outside the workspace rooted
// at root.
func escapesRoot(root, path string) bool {
	return !strings.HasPrefix(path, root+"/")
}

// resolveEscapingSymlink follows a symlink chain that escapes root,
// resolving through external targets (e.g. ../.cipd/pkgs/..), and
// returns me updated with the final resolved data. The caller must
// verify that e is a symlink whose first hop escapes root.
func (hfs *HashFS) resolveEscapingSymlink(ctx context.Context, root, fname string, e *entry, me merkletree.Entry) (merkletree.Entry, error) {
	name := filepath.Join(root, fname)
	elink := e
	for range maxSymlinks {
		tname := makeFullpath(filepath.Dir(name), elink.target)
		if log.V(1) {
			clog.Infof(ctx, "symlink %s -> %s", name, tname)
		}
		if !escapesRoot(root, tname) {
			break
		}
		// symlink to outside of workspace (e.g. ../.cipd/pkgs/..)
		name = tname
		var ok bool
		elink, _, _, ok = hfs.directory.lookup(ctx, name)
		if ok {
			if log.V(2) {
				clog.Infof(ctx, "tree cache hit %s", name)
			}
		} else {
			elink = newLocalEntry()
			elink.init(ctx, name, hfs.executables, hfs.OS)
			if log.V(1) {
				clog.Infof(ctx, "tree new entry %s", name)
			}
			var err error
			elink, err = hfs.directory.store(ctx, name, elink)
			if err != nil {
				return merkletree.Entry{}, err
			}
			hfs.digester.lazyCompute(ctx, name, elink)
		}
		if elink.err != nil || !elink.isSymlink() {
			break
		}
	}
	clog.Infof(ctx, "resolve symlink %s to %s", fname, name)
	hfs.digester.compute(ctx, name, elink)
	d := elink.digest()
	me.Data = digest.NewData(elink.src, d)
	me.IsExecutable = elink.mode&0111 != 0
	me.Target = elink.target
	return me, nil
}

// Entries gets merkletree entries for inputs at root.
// it won't return entries symlink escaped from root.
// root can be an empty string "" when inputs are absolute paths.
func (hfs *HashFS) Entries(ctx context.Context, root string, inputs []string) ([]merkletree.Entry, error) {
	ctx, span := trace.NewSpan(ctx, "fs-entries")
	defer span.Close(nil)
	var nwait int
	var wg sync.WaitGroup
	ents := make([]*entry, 0, len(inputs))
	for _, fname := range inputs {
		fname := makeFullpath(root, fname)
		e, _, _, ok := hfs.directory.lookup(ctx, fname)
		if ok {
			if log.V(2) {
				clog.Infof(ctx, "tree cache hit %s", fname)
			}
			ents = append(ents, e)
			if e.mode.IsRegular() {
				e.mu.RLock()
				ready := !e.d.IsZero()
				e.mu.RUnlock()
				if !ready {
					wg.Add(1)
					nwait++
					go func() {
						defer wg.Done()
						hfs.digester.compute(ctx, fname, e)
					}()
				}
			}
			continue
		}
		e = newLocalEntry()
		e.init(ctx, fname, hfs.executables, hfs.OS)
		if errors.Is(e.err, context.Canceled) {
			return nil, e.err
		}
		if log.V(1) {
			clog.Infof(ctx, "tree new entry %s", fname)
		}
		ee, err := hfs.directory.store(ctx, fname, e)
		if err != nil {
			// store may fail for missing dir. b/468142748
			clog.Warningf(ctx, "entry %s %v: %v", fname, e.err, err)
			ents = append(ents, e)
			continue
		}
		e = ee
		ents = append(ents, e)
		wg.Add(1)
		nwait++
		go func() {
			defer wg.Done()
			hfs.digester.compute(ctx, fname, e)
		}()
	}
	// wait ensures all entries have computed the digests.
	_, wspan := trace.NewSpan(ctx, "fs-entries-wait")
	wg.Wait()
	wspan.SetAttr("waits", nwait)
	wspan.Close(nil)
	entries := make([]merkletree.Entry, 0, len(inputs))
	for i, fname := range inputs {
		e := ents[i]
		d := e.digest()
		if e.err != nil || (d.IsZero() && !e.isSymlink() && e.directory == nil) {
			// TODO(b/435555841): hard fail instead
			if e.entryErrLogged.CompareAndSwap(false, true) {
				clog.Warningf(ctx, "missing %s data:%v target:%q: %v", fname, e.d, e.target, e.err)
			}
			continue
		}
		me := merkletree.Entry{
			Name:         fname,
			Data:         digest.NewData(e.src, d),
			IsExecutable: e.mode&0111 != 0,
			Target:       e.target,
		}
		if e.isSymlink() {
			name := filepath.Join(root, fname)
			tname := makeFullpath(filepath.Dir(name), e.target)
			if escapesRoot(root, tname) {
				var err error
				me, err = hfs.resolveEscapingSymlink(ctx, root, fname, e, me)
				if err != nil {
					return nil, err
				}
			}
		}
		entries = append(entries, me)
	}
	return entries, nil
}

// UpdateEntry is an entry for Update.
type UpdateEntry struct {
	Name string

	// if Entry is nil, use local disk (from RetrieveUpdateEntriesFromLocal), so need to calculate digest from file.
	// If Entry is not nil, use digest in Entry, rather than calculating digest from file.
	Entry *merkletree.Entry

	Mode        fs.FileMode
	ModTime     time.Time
	CmdHash     []byte
	EdgeHash    []byte
	Action      digest.Digest
	UpdatedTime time.Time
	IsChanged   bool

	// IsLocal=true uses Entry, but assumes file exists on local,
	// to avoid unnecessary flush operation.
	IsLocal bool
}

func (e UpdateEntry) String() string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "%q ", e.Name)
	if e.Entry != nil {
		switch {
		case !e.Entry.Data.IsZero():
			fmt.Fprintf(&buf, "file=%s ", e.Entry.Data.Digest())
		case e.Entry.Target != "":
			fmt.Fprintf(&buf, "symlink=%s ", e.Entry.Target)
		default:
			fmt.Fprintf(&buf, "dir ")
		}
	} else {
		fmt.Fprintf(&buf, "local ")
	}
	fmt.Fprintf(&buf, "mode=%s ", e.Mode)
	fmt.Fprintf(&buf, "mtime=%s ", e.ModTime.Format(time.RFC3339Nano))
	fmt.Fprintf(&buf, "cmdhash=%s ", base64.StdEncoding.EncodeToString(e.CmdHash))
	fmt.Fprintf(&buf, "edgehash=%s ", base64.StdEncoding.EncodeToString(e.EdgeHash))
	fmt.Fprintf(&buf, "action=%s ", e.Action)
	if !e.ModTime.Equal(e.UpdatedTime) {
		fmt.Fprintf(&buf, "updated_time=%s ", e.UpdatedTime.Format(time.RFC3339Nano))
	}
	fmt.Fprintf(&buf, "changed=%t is_local=%t", e.IsChanged, e.IsLocal)
	return buf.String()
}

// newEntryFromUpdate creates an entry from an UpdateEntry that has
// a non-nil Entry (i.e. from remote execution, not local disk).
func newEntryFromUpdate(ent UpdateEntry) *entry {
	lready := make(chan bool, 1)
	if ent.IsLocal {
		close(lready)
	} else {
		lready <- true
	}
	e := &entry{
		lready:      lready,
		mtime:       ent.ModTime,
		mode:        ent.Mode,
		cmdhash:     ent.CmdHash,
		edgehash:    ent.EdgeHash,
		action:      ent.Action,
		updatedTime: ent.UpdatedTime,
		isChanged:   ent.IsChanged,
	}
	switch {
	case !ent.Entry.Data.IsZero():
		if ent.Entry.IsExecutable {
			e.mode |= 0111
		}
		e.size = ent.Entry.Data.Digest().SizeBytes
		e.local = ent.IsLocal
		e.src = ent.Entry.Data
		e.d = ent.Entry.Data.Digest()
	case ent.Entry.Target != "":
		e.mode |= fs.ModeSymlink
		e.local = true
		e.target = ent.Entry.Target
	default: // directory
		e.mode |= fs.ModeDir
		e.local = true
		e.directory = &directory{}
	}
	return e
}

// Update updates cache information for entries under workspaceRoot.
func (hfs *HashFS) Update(ctx context.Context, workspaceRoot string, entries []UpdateEntry) error {
	ctx, span := trace.NewSpan(ctx, "fs-update")
	defer span.Close(nil)
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	default:
	}
	hfs.clean.Store(false)

	// sort inputs, so update dir containing files first. b/300385880
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	if hfs.opt.CogFS != nil || hfs.opt.ArtFS != nil {
		// TODO: pass UpdateEntry so artfs can set mtime?
		var updates []merkletree.Entry
		var updateIdx []int
		var nFromLocals, nNonFiles int
		for i, ent := range entries {
			if ent.Entry == nil {
				// UpdateEntry was captured by RetrieveUpdateEntriesFromLocal
				// so file already exist on local disk
				nFromLocals++
				continue
			}
			if ent.Entry.Data.IsZero() {
				// symlink or dir. handled in usual way.
				nNonFiles++
				continue
			}
			updateIdx = append(updateIdx, i)
			updates = append(updates, *ent.Entry)
		}
		if len(updates) > 0 {
			err := hfs.opt.ArtFS.ArtfsInsert(ctx, workspaceRoot, updates)
			if err != nil {
				clog.Warningf(ctx, "artfs insert %d under %s: %v", len(updates), workspaceRoot, err)
			} else {
				clog.Infof(ctx, "artfs insert %d under %s", len(updates), workspaceRoot)
				// artfsfs inserted the update, so we can assume
				// these files exist locally.
				for _, i := range updateIdx {
					entries[i].IsLocal = true
				}
			}
		} else {
			clog.Warningf(ctx, "artfs insert 0 from_local=%d not_file=%d", nFromLocals, nNonFiles)
		}
	}

	for _, ent := range entries {
		clog.Infof(ctx, "update %v", ent)
		fname := filepath.Join(workspaceRoot, ent.Name)
		fname = filepath.ToSlash(fname)
		if ent.Entry == nil {
			// UpdateEntry was captured by RetrieveUpdateEntriesFromLocal
			// so the entry should exists in hfs.directory.
			e, _, _, ok := hfs.dirLookup(ctx, workspaceRoot, ent.Name)
			if !ok {
				clog.Warningf(ctx, "failed to update: no entry %s", ent.Name)
				continue
			}
			if e.getMtime().Equal(ent.ModTime) || e.getDir() != nil {
				e.mu.Lock()
				e.mtime = ent.ModTime
				e.mode = ent.Mode
				e.cmdhash = ent.CmdHash
				e.edgehash = ent.EdgeHash
				e.action = ent.Action
				e.local = ent.IsLocal
				e.updatedTime = ent.UpdatedTime
				e.isChanged = ent.IsChanged
				e.entryErrLogged.Store(false)
				e.mu.Unlock()
			} else {
				e = newLocalEntry()
				e.init(ctx, fname, hfs.executables, hfs.OS)
				e.mtime = ent.ModTime
				e.cmdhash = ent.CmdHash
				e.edgehash = ent.EdgeHash
				e.action = ent.Action
				e.local = ent.IsLocal
				e.updatedTime = ent.UpdatedTime
				e.isChanged = ent.IsChanged
			}
			if errors.Is(e.err, context.Canceled) {
				return e.err
			}
			// notify this output to scandeps.
			err := hfs.dirStoreAndNotify(ctx, fname, e)
			if err != nil {
				return err
			}
			hfs.journalEntry(ctx, fname, e)
			if ent.IsLocal && e.isChanged && !e.isSymlink() {
				// Update mtime for the local entry if it has changed.
				// Don't update mtime for symlink,
				// since os.Chtimes updates the mtime of target
				// and it makes the target invalidated
				// in .siso_fs_state since mtime doesn't match.
				err := hfs.OS.Chtimes(ctx, fname, time.Time{}, e.getMtime())
				if errors.Is(err, fs.ErrNotExist) {
					clog.Warningf(ctx, "failed to update mtime of %s: %v", fname, err)
					continue
				}
				if err != nil {
					return fmt.Errorf("failed to update mtime of %s: %w", fname, err)
				}
			}
			continue
		}
		e := newEntryFromUpdate(ent)
		err := hfs.dirStoreAndNotify(ctx, fname, e)
		if err != nil {
			return err
		}
		hfs.journalEntry(ctx, fname, e)
		if e.directory != nil {
			err = hfs.OS.Chtimes(ctx, fname, time.Time{}, ent.ModTime)
			if err != nil {
				clog.Warningf(ctx, "failed to update dir mtime %s: %v", fname, err)
			}
		} else if ent.IsLocal && e.isChanged && !e.isSymlink() {
			err = hfs.OS.Chtimes(ctx, fname, time.Time{}, e.getMtime())
			if errors.Is(err, fs.ErrNotExist) {
				clog.Warningf(ctx, "failed to update mtime of %s: %v", fname, err)
				continue
			}
			if err != nil {
				return fmt.Errorf("failed to update mtime of %s: %w", fname, err)
			}
		}
	}
	return nil
}

// RetrieveUpdateEntries gets UpdateEntry for fnames at root.
func (hfs *HashFS) RetrieveUpdateEntries(ctx context.Context, root string, fnames []string) []UpdateEntry {
	ctx, span := trace.NewSpan(ctx, "fs-update-entries")
	defer span.Close(nil)
	ents, err := hfs.Entries(ctx, root, fnames)
	if err != nil {
		clog.Warningf(ctx, "failed to get entries: %v", err)
	}
	entries := make([]UpdateEntry, 0, len(ents))
	for _, ent := range ents {
		fi, err := hfs.Stat(ctx, root, ent.Name)
		if err != nil {
			clog.Warningf(ctx, "failed to stat %s: %v", ent.Name, err)
			continue
		}
		entries = append(entries, UpdateEntry{
			Name:        ent.Name,
			Entry:       &ent,
			Mode:        fi.Mode(),
			ModTime:     fi.ModTime(),
			CmdHash:     fi.CmdHash(),
			EdgeHash:    fi.EdgeHash(),
			Action:      fi.Action(),
			UpdatedTime: fi.UpdatedTime(),
			IsChanged:   fi.IsChanged(),
		})
	}
	return entries
}

// RetrieveUpdateEntriesFromLocal gets UpdateEntry for fnames at root from local disk.
// It is intended to be used for local execution outputs in cmd.RecordOutputsFromLocal.
// It won't wait for digest calculation for entries, so UpdateEntry's Entry
// will be nil.
// It will forget recorded enties when err (doesn't exist or so).
func (hfs *HashFS) RetrieveUpdateEntriesFromLocal(ctx context.Context, root string, fnames []string) []UpdateEntry {
	ctx, span := trace.NewSpan(ctx, "fs-update-entries-from-local")
	defer span.Close(nil)

	ents := make([]UpdateEntry, 0, len(fnames))
	// invalidate hashfs cache for all fnames and its missing parents.
	for _, fname := range fnames {
		fullname := makeFullpath(root, fname)
		lfi, err := hfs.OS.Lstat(ctx, fullname)
		if errors.Is(err, fs.ErrNotExist) {
			clog.Warningf(ctx, "missing local %s: %v", fname, err)
			hfs.directory.delete(ctx, fullname)
			continue
		} else if err != nil {
			clog.Warningf(ctx, "failed to access local %s: %v", fname, err)
			hfs.directory.delete(ctx, fullname)
			continue
		}
		if !lfi.IsDir() {
			// forget old entries unless dir.
			// need to keep dir to keep other files in the dir.
			hfs.directory.delete(ctx, fullname)
		}
		// clear negative cache in parent directories
		pathname := filepath.ToSlash(filepath.Dir(fullname))
		for {
			_, lerr := hfs.OS.Lstat(ctx, pathname)
			if errors.Is(lerr, fs.ErrNotExist) {
				// if local dir doesn't exist,
				// no need to invalidate hashfs dir.
				break
			}
			_, err = hfs.Stat(ctx, "", pathname)
			if errors.Is(err, lerr) {
				// if err matches with local err,
				// no need to invalidate hashfs dir.
				break
			}
			hfs.directory.delete(ctx, pathname)
			parent := filepath.ToSlash(filepath.Dir(pathname))
			if parent == pathname || parent == "/" || parent == "" || parent == "." {
				// nothing to do more.
				break
			}
			pathname = parent
		}
		ent := UpdateEntry{
			Name:    fname,
			Mode:    lfi.Mode(),
			ModTime: lfi.ModTime(),
			IsLocal: true,
		}
		ents = append(ents, ent)
	}
	// capture hashfs for all fnames after all missing entries, parents
	// are invalidated in the above loop.
	for i, ent := range ents {
		fi, err := hfs.Stat(ctx, root, ent.Name)
		if err != nil {
			clog.Warningf(ctx, "failed to stat after invalidate %s: %v", ent.Name, err)
		} else {
			ent.CmdHash = fi.CmdHash()
			ent.EdgeHash = fi.EdgeHash()
			ent.Action = fi.Action()
			ent.UpdatedTime = fi.UpdatedTime()
			ent.IsChanged = fi.IsChanged()
			ents[i] = ent
		}
	}
	return ents
}

type noSource struct {
	filename string
}

func (ns noSource) Open(ctx context.Context) (io.ReadCloser, error) {
	return nil, fmt.Errorf("no source for %s", ns.filename)
}

func (ns noSource) String() string {
	return fmt.Sprintf("noSource:%s", ns.filename)
}

type noDataSource struct{}

func (noDataSource) Source(_ context.Context, d digest.Digest, fname string) digest.Source {
	return noSource{fname}
}

// NeedFlush returns whether the fname need to be flushed based on OutputLocal option.
func (hfs *HashFS) NeedFlush(ctx context.Context, workspaceRoot, fname string) bool {
	return hfs.opt.OutputLocal(ctx, makeFullpath(workspaceRoot, fname))
}

// Flush flushes cached information for files under workspaceRoot to local disk.
func (hfs *HashFS) Flush(ctx context.Context, workspaceRoot string, files []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx, span := trace.NewSpan(ctx, "flush")
	defer span.Close(nil)
	eg, ctx := errgroup.WithContext(ctx)
	for _, file := range files {
		fname := makeFullpath(workspaceRoot, file)
		e, _, _, ok := hfs.directory.lookup(ctx, fname)
		if !ok {
			// If it doesn't exist in memory, just use local disk as is.
			continue
		}
		select {
		case need := <-e.lready:
			if !need {
				// need=false means file is already downloaded,
				// or entry was constructed from local disk.
				if log.V(1) {
					clog.Infof(ctx, "flush %s local ready", fname)
				}
				e.mu.Lock()
				if e.mtimeUpdated && !e.isSymlink() {
					// mtime was updated after entry sets mtime from the local disk.
					// Don't update mtime for symlink,
					// since os.Chtimes updates the mtime of target
					// and it makes the target invalidated
					// in .siso_fs_state since mtime doesn't match.
					err := hfs.OS.Chtimes(ctx, fname, time.Time{}, e.mtime)
					if errors.Is(err, fs.ErrNotExist) {
						e.mu.Unlock()
						return fmt.Errorf("flush %s local-ready: %w", fname, err)
					}
					clog.Infof(ctx, "flush %s local ready mtime update: %v", fname, err)
					if err == nil {
						e.mtimeUpdated = false
					}
				}
				err := e.err
				e.mu.Unlock()
				if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errNotRegular) {
					clog.Warningf(ctx, "flush %s local-ready: %v", fname, err)
					continue
				}
				if err != nil {
					return fmt.Errorf("flush %s local-ready: %w", fname, err)
				}
				continue
			}
		case <-ctx.Done():
			return fmt.Errorf("flush wait local-ready %s: %w", fname, context.Cause(ctx))
		}
		hfs.digester.compute(ctx, fname, e)
		ctx, done, err := FlushSemaphore.WaitAcquire(ctx)
		if err != nil {
			// flush failed, so may need to flush again.
			select {
			case e.lready <- true:
			default:
			}
			return fmt.Errorf("flush semaphore %s: %w", fname, err)
		}
		eg.Go(func() (err error) {
			defer func() { done(err) }()
			err = e.flush(ctx, fname, hfs.OS, max(e.d.FetchTimeout(), hfs.opt.MinFlushTimeout))
			// flush should not fail with cas not found error.
			// but if it failed, current recorded digest should
			// be wrong, so should delete from the hashfs.
			if code := status.Code(err); code == codes.NotFound {
				clog.Warningf(ctx, "flush failed. delete %s from hashfs: %v", fname, err)
				hfs.directory.delete(ctx, fname)
			}
			return err
		})
	}
	return eg.Wait()
}

// Refresh refreshes cached file entries.
func (hfs *HashFS) Refresh(ctx context.Context) error {
	// TODO: optimize?
	state := hfs.State(ctx)
	// reset loaded as it reset entry data.
	hfs.loaded.Store(false)
	hfs.directory = &directory{isRoot: true}
	err := hfs.SetState(ctx, state)
	werr := hfs.WaitReady(ctx)
	if err != nil {
		return err
	}
	return werr
}

// FileInfo implements https://pkg.go.dev/io/fs#FileInfo.
type FileInfo struct {
	root  string
	fname string
	e     *entry
	fis   []FileInfo
}

func (fi FileInfo) Path() string {
	return makeFullpath(fi.root, fi.fname)
}

// Name is a base name of the file.
func (fi FileInfo) Name() string {
	return filepath.Base(fi.fname)
}

// Size is a size of the file.
func (fi FileInfo) Size() int64 {
	return fi.e.size
}

// Mode is a file mode of the file.
func (fi FileInfo) Mode() fs.FileMode {
	return fi.e.mode
}

// ModTime is a modification time of the file.
func (fi FileInfo) ModTime() time.Time {
	return fi.e.getMtime()
}

// UpdatedTime is a update time of the file.
// Usually it is the same with ModTime, but may differ for restat=1.
func (fi FileInfo) UpdatedTime() time.Time {
	return fi.e.getUpdatedTime()
}

// IsChanged returns true if file has been changed in the session.
func (fi FileInfo) IsChanged() bool {
	fi.e.mu.RLock()
	defer fi.e.mu.RUnlock()
	return fi.e.isChanged
}

// IsMissingChecked returns true if file has been checked existence
// for ForgetMissings.
func (fi FileInfo) IsMissingChecked() bool {
	fi.e.mu.RLock()
	defer fi.e.mu.RUnlock()
	return fi.e.isMissingChecked
}

// IsDir returns true if it is the directory.
func (fi FileInfo) IsDir() bool {
	return fi.e.mode.IsDir()
}

// Sys returns merkletree Entry of the file.
// For local file, digest may not be calculated yet.
// Use Entries to get correct merkletree.Entry.
func (fi FileInfo) Sys() any {
	d := fi.e.digest()
	return merkletree.Entry{
		Name:         fi.Path(),
		Data:         digest.NewData(fi.e.src, d),
		IsExecutable: fi.e.mode&0111 != 0,
		Target:       fi.e.target,
	}
}

// CmdHash returns a cmdhash that created the file.
func (fi FileInfo) CmdHash() []byte {
	return fi.e.cmdhash
}

// EdgeHash returns a edgehash that created the file.
func (fi FileInfo) EdgeHash() []byte {
	return fi.e.edgehash
}

// Action returns a digest of action that created the file.
func (fi FileInfo) Action() digest.Digest {
	return fi.e.action
}

// Target returns a symlink target of the file, or empty if it is not symlink.
func (fi FileInfo) Target() string {
	return fi.e.target
}

// Symlinks returns a symlink's FileInfo to resolve the file.
func (fi FileInfo) Symlinks() []FileInfo {
	return fi.fis
}

// DirEntry implements https://pkg.go.dev/io/fs#DirEntry.
type DirEntry struct {
	fi FileInfo
}

// Name is a base name in the directory.
func (de DirEntry) Name() string {
	return de.fi.Name()
}

// IsDir returns true if it is a directory.
func (de DirEntry) IsDir() bool {
	return de.fi.IsDir()
}

// Type returns a file type.
func (de DirEntry) Type() fs.FileMode {
	return de.fi.Mode().Type()
}

// Info returns a FileInfo.
func (de DirEntry) Info() (fs.FileInfo, error) {
	return de.fi, nil
}

// waitUntilModTime ensures mtime is past timestamp
// for clock stepback by NTP.
func waitUntilModTime(ctx context.Context, fullname string, mtime time.Time) error {
	now := time.Now()
	if !mtime.After(now) {
		return nil
	}
	if mtime.Sub(now) > 5*time.Second {
		return fmt.Errorf("future mtime on %s: mtime=%s now=%s", fullname, mtime, now)
	}
	// report error as it implies that broken time synchronization
	// so unreliable build.
	clog.Errorf(ctx, "waiting for future mtime on %s: mtime=%s now=%s", fullname, mtime, now)
	started := now
	for time.Since(started) < 5*time.Second {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(min(5*time.Second, mtime.Sub(now))):
		}
		now = time.Now()
		if !mtime.After(now) {
			clog.Warningf(ctx, "future mtime corrected for %s in %s", fullname, time.Since(started))
			return nil
		}
	}
	return fmt.Errorf("future mtime on %s: mtime=%s now=%s %s", fullname, mtime, started, now)
}
