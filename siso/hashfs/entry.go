// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	log "github.com/golang/glog"

	"go.chromium.org/build/siso/hashfs/osfs"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/reapi/digest"
	"go.chromium.org/build/siso/reapi/retry"
)

// entry represents a file, directory, or symlink in hashfs.
//
// The type of entry is determined by the following invariants:
//   - directory: directory is not nil. target must be "" and d must be zero.
//   - symlink: target is not "". directory must be nil and d must be zero.
//   - file: directory is nil and target is "". d may be zero
//     (if digest has not been calculated yet).
type entry struct {
	// lready represents whether it is ready to use local file.
	// true - need to download contents.
	// block - download is in progress.
	// closed/false - file is already downloaded.
	lready chan bool

	err  error
	size int64
	mode fs.FileMode

	// cmdhash is hash of command lines that generated this file.
	// e.g. hash('touch output.stamp')
	cmdhash []byte

	// edgehash is hash of inputs/outputs paths that generated this file.
	edgehash []byte

	// digest of action that generated this file.
	action digest.Digest

	// local indicates the file is generated locally.
	local bool
	// isChanged indicates the file is changed in the session.
	isChanged bool
	// isMissingChecked indicates the file is checked in ForgetMissings.
	isMissingChecked bool

	target string // symlink.

	src digest.Source
	buf []byte // from WriteFile.

	mu sync.RWMutex
	// mtime of entry in hashfs.
	mtime        time.Time
	mtimeUpdated bool
	// updatedTime is timestamp when the file has been updated
	// by Update or UpdateFromLocal.
	// need to distinguish from mtime for restat=1.
	// updatedTime should be equal or newer than mtime.
	updatedTime time.Time

	entryErrLogged atomic.Bool

	d         digest.Digest
	dch       chan struct{} // wait for digest computation
	directory *directory
}

func newLocalEntry() *entry {
	lready := make(chan bool)
	close(lready)
	return &entry{
		lready: lready,
	}
}

func (e *entry) String() string {
	e.mu.Lock()
	err := e.err
	e.mu.Unlock()
	if err != nil {
		return fmt.Sprintf("err:%v", err)
	}
	return fmt.Sprintf("size:%d mode:%s mtime:%s", e.size, e.mode, e.getMtime())
}

var errNotRegular = errors.New("unexpected filetype not regular")

func (e *entry) init(ctx context.Context, fname string, executables map[string]bool, osfs *osfs.OSFS) {
	fi, err := osfs.Lstat(ctx, fname)
	if errors.Is(err, fs.ErrNotExist) {
		if log.V(1) {
			clog.Infof(ctx, "not exist %s", fname)
		}
		e.err = err
		return
	}
	if err != nil {
		clog.Warningf(ctx, "failed to lstat %s: %v", fname, err)
		e.err = err
		return
	}
	err = waitUntilModTime(ctx, fname, fi.ModTime())
	if err != nil {
		e.err = err
		return
	}
	switch {
	case fi.IsDir():
		if log.V(1) {
			clog.Infof(ctx, "tree entry %s: is dir", fname)
		}
		e.directory = &directory{}
		e.mode = 0644 | fs.ModeDir
	case fi.Mode().Type() == fs.ModeSymlink:
		e.mode = 0644 | fs.ModeSymlink
		e.target, err = osfs.Readlink(ctx, fname)
		if err != nil {
			e.err = err
		}
		if log.V(1) {
			clog.Infof(ctx, "tree entry %s: symlink to %s: %v", fname, e.target, e.err)
		}
	case fi.Mode().IsRegular():
		e.mode = 0644
		if isExecutable(fi, fname, executables) {
			e.mode |= 0111
		}
		e.size = fi.Size()
		e.src = osfs.FileSource(fname, fi.Size())
	default:
		// e.g. fifo in chromiumos build tree?
		// /build/amd64-generic/tmp/portage/chromeos-base/chromeos-chrome-139.0.7206.0_rc-r1/.ipc/in: unknown filetype prwxrwx---
		e.err = fmt.Errorf("entry %s: %s: %w", fname, fi.Mode(), errNotRegular)
		clog.Warningf(ctx, "tree entry %s: unknown filetype %s", fname, fi.Mode())
		return
	}
	if e.mtime.Before(fi.ModTime()) {
		e.mtime = fi.ModTime()
	}
	if e.updatedTime.Before(e.mtime) {
		e.updatedTime = e.mtime
	}
}

func (e *entry) compute(ctx context.Context, fname string) error {
	needCompute, doCompute, err := func() (bool, bool, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.err != nil {
			return false, false, e.err
		}
		if !e.d.IsZero() {
			return false, false, nil
		}
		if e.src == nil {
			return false, false, nil
		}
		doCompute := e.dch == nil
		if doCompute {
			e.dch = make(chan struct{})
		}
		return true, doCompute, nil
	}()
	if !needCompute {
		return err
	}
	if doCompute {
		type res struct {
			data digest.Data
			err  error
		}
		ch := make(chan res, 1)
		go func() {
			data, err := localDigest(ctx, e.src, fname)
			ch <- res{data: data, err: err}
		}()
		select {
		case <-ctx.Done():
			err := context.Cause(ctx)
			e.mu.Lock()
			close(e.dch)
			e.err = err
			e.entryErrLogged.Store(false)
			e.mu.Unlock()
			return err
		case r := <-ch:
			if r.err != nil {
				e.mu.Lock()
				close(e.dch)
				e.err = r.err
				e.entryErrLogged.Store(false)
				e.mu.Unlock()
				return r.err
			}
			e.mu.Lock()
			e.d = r.data.Digest()
			close(e.dch)
			e.entryErrLogged.Store(false)
			e.mu.Unlock()
		}
		return nil
	}
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-e.dch:
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

func (e *entry) digest() digest.Digest {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.d
}

func (e *entry) getMtime() time.Time {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mtime
}

func (e *entry) getUpdatedTime() time.Time {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.updatedTime
}

func (e *entry) updateDir(ctx context.Context, hfs *HashFS, dname string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	defer e.entryErrLogged.Store(false)
	d, err := os.Open(dname)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			clog.Warningf(ctx, "updateDir %s: open %v", dname, err)
		}
		return nil
	}
	defer d.Close()
	fi, err := d.Stat()
	if err != nil {
		clog.Warningf(ctx, "updateDir %s: stat %v", dname, err)
		return nil
	}
	if !fi.IsDir() {
		clog.Warningf(ctx, "updateDir %s: is not dir?", dname)
		return nil
	}
	if fi.ModTime().Equal(e.directory.mtime) {
		if log.V(1) {
			clog.Infof(ctx, "updateDir %s: up-to-date %s", dname, e.mtime)
		}
		return nil
	}
	started := time.Now()
	names, err := d.Readdirnames(-1)
	if err != nil {
		clog.Warningf(ctx, "updateDir %s: readdirnames %v", dname, err)
		return nil
	}
	if hfs.OnCog() {
		var wg sync.WaitGroup
		for _, name := range names {
			// don't scan temporary file by readdir.
			// it may cause race on windows.
			// b/294318963 b/381947692
			if strings.HasSuffix(name, ".tmp") || strings.HasPrefix(name, ".tempfile.") {
				continue
			}
			if hfs.opt.Ignore(ctx, filepath.Join(dname, name)) {
				continue
			}
			wg.Go(func() {
				// update entry in e.directory.
				_, err := hfs.stat(ctx, dname, name, false)
				if err != nil {
					clog.Warningf(ctx, "updateDir stat %s: %v", name, err)
				}
			})
		}
		wg.Wait()
	} else {
		for _, name := range names {
			// don't scan temporary file by readdir.
			// it may cause race on windows.
			// b/294318963 b/381947692
			if strings.HasSuffix(name, ".tmp") || strings.HasPrefix(name, ".tempfile.") {
				continue
			}
			if hfs.opt.Ignore(ctx, filepath.Join(dname, name)) {
				continue
			}
			// update entry in e.directory.
			_, err := hfs.stat(ctx, dname, name, false)
			if err != nil {
				clog.Warningf(ctx, "updateDir stat %s: %v", name, err)
			}
		}
	}
	clog.Infof(ctx, "updateDir mtime %s %d %s -> %s: %s", dname, len(names), e.directory.mtime, fi.ModTime(), time.Since(started))
	e.directory.mtime = fi.ModTime()
	// if local dir is updated after hashfs update, update hashfs mtime.
	if e.mtime.Before(e.directory.mtime) {
		e.mtime = e.directory.mtime
	}
	return names
}

// getDir returns directory of entry.
func (e *entry) getDir() *directory {
	if e == nil {
		return nil
	}
	return e.directory
}

// isSymlink returns whether the entry is a symlink.
func (e *entry) isSymlink() bool {
	return e.target != ""
}

func (e *entry) flush(ctx context.Context, fname string, osfs *osfs.OSFS, timeout time.Duration) (retErr error) {
	defer func() {
		if retErr != nil {
			// flush failed, so may need to flush again.
			clog.Warningf(ctx, "failed to flush %s: %v", fname, retErr)
			select {
			case e.lready <- true:
			default:
			}
			return
		}
		// flush successfully completed.
		// no need to flush again.
		close(e.lready)
	}()
	started := time.Now()

	e.mu.Lock()
	err := e.err
	e.mu.Unlock()
	if errors.Is(err, fs.ErrNotExist) {
		// to protect concurrent digest calculation and removal
		// on Windows.
		digestLock.Lock()
		for {
			if _, ok := digestFnames[fname]; !ok {
				break
			}
			// wait if digest calculation on fname is under progress
			digestCond.Wait()
		}
		err := osfs.Remove(ctx, fname)
		digestLock.Unlock()
		clog.Infof(ctx, "flush remove %s: %v", fname, err)
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		return err
	}
	d := e.digest()
	mtime := e.getMtime()
	switch {
	case e.directory != nil:
		// directory
		fi, err := osfs.Lstat(ctx, fname)
		if err == nil && fi.IsDir() && fi.ModTime().Equal(mtime) {
			if log.V(1) {
				clog.Infof(ctx, "flush dir %s: already exist", fname)
			}
			return nil
		}
		err = osfs.MkdirAll(ctx, fname, 0755)
		if err != nil {
			clog.Infof(ctx, "flush dir %s: %v", fname, err)
		} else {
			err = osfs.Chtimes(ctx, fname, time.Time{}, mtime)
			clog.Infof(ctx, "flush dir chtime %s %v: %v", fname, mtime, err)
		}
		return err
	case e.isSymlink():
		target, err := osfs.Readlink(ctx, fname)
		if err == nil && e.target == target {
			return nil
		}
		e.mu.Lock()
		err = osfs.Symlink(ctx, e.target, fname)
		if errors.Is(err, fs.ErrExist) {
			err = osfs.Remove(ctx, fname)
			if err == nil {
				err = osfs.Symlink(ctx, e.target, fname)
			}
		}
		e.mu.Unlock()
		clog.Infof(ctx, "flush symlink %s -> %s: %v", fname, e.target, err)
		// don't change mtimes. it fails if target doesn't exist.
		return err
	default:
	}
	fi, err := osfs.Lstat(ctx, fname)
	// need to remove the file after it reads from data source,
	// since data source will read from the local disk.
	var removeReason string
	if err == nil {
		if fi.IsDir() {
			err := &fs.PathError{
				Op:   "flush",
				Path: fname,
				Err:  syscall.EISDIR,
			}
			clog.Warningf(ctx, "flush %s: %v", fname, err)
			return err
		}
		if fi.Size() == d.SizeBytes && fi.ModTime().Equal(mtime) {
			// TODO: check hash, mode?
			clog.Infof(ctx, "flush %s: already exist", fname)
			return nil
		}
		if isHardlink(fi) {
			removeReason = "hardlink"
		} else if !fi.Mode().IsRegular() {
			removeReason = fmt.Sprintf("non-regular file %s", fi.Mode())
		} else if fi.Size() == d.SizeBytes {
			// check existing file and hashfs entry are identical
			// or not by checking digest.
			// if size differs, no need to check and force
			// to write hashfs entry to the disk.
			var fileDigest digest.Digest
			src := osfs.FileSource(fname, fi.Size())
			ld, err := localDigest(ctx, src, fname)
			if err == nil {
				fileDigest = ld.Digest()
				if fileDigest == d {
					if log.V(1) {
						clog.Infof(ctx, "flush %s: already exist - hash match", fname)
					}
					if !fi.ModTime().Equal(mtime) {
						err = osfs.Chtimes(ctx, fname, time.Time{}, mtime)
					}
					return err
				}
			}
			clog.Warningf(ctx, "flush %s: exists but mismatch size:%d!=%d mtime:%s!=%s d:%v!=%v", fname, fi.Size(), d.SizeBytes, fi.ModTime(), mtime, fileDigest, d)
		}
		if removeReason == "" && fi.Mode()&0200 == 0 {
			// need to be writable. otherwise os.WriteFile fails with permission denied.
			err = osfs.Chmod(ctx, fname, fi.Mode()|0200)
			clog.Warningf(ctx, "flush %s: not writable? %s: %v", fname, fi.Mode(), err)
		}
	}
	err = osfs.MkdirAll(ctx, filepath.Dir(fname), 0755)
	if err != nil {
		clog.Warningf(ctx, "flush %s: mkdir: %v", fname, err)
		return fmt.Errorf("failed to create directory for %s: %w", fname, err)
	}
	if d.SizeBytes == 0 {
		if removeReason != "" {
			err = osfs.Remove(ctx, fname)
			clog.Infof(ctx, "flush %s: remove %s: %v", fname, removeReason, err)
		}
		clog.Infof(ctx, "flush %s: empty file", fname)
		err := osfs.WriteFile(ctx, fname, nil, 0644)
		if err != nil {
			return err
		}
		err = osfs.Chtimes(ctx, fname, time.Time{}, mtime)
		if err != nil {
			return err
		}
		return nil
	}
	buf := e.buf
	removeBeforeWrite := func() {
		if removeReason != "" {
			err = osfs.Remove(ctx, fname)
			clog.Infof(ctx, "flush %s: remove %s: %v", fname, removeReason, err)
		}
	}
	if len(buf) == 0 {
		if e.d.IsZero() {
			return fmt.Errorf("no data: retrieve %s: ", fname)
		}
		err = func() error {
			// check if hashfs entry is copy of local file,
			// i.e. created by hashfs Copy method.
			// if hashfs entry is set by remote action,
			// it would not be osfs.FileSource
			lsrc, ok := osfs.AsFileSource(e.src)
			type clonefiler interface {
				Clonefile(context.Context, string, string) error
			}
			var osfsany any = osfs
			osfsc, cok := osfsany.(clonefiler)
			if ok && cok {
				if lsrc.Fname == fname {
					err = osfs.Chmod(ctx, fname, e.mode)
					return err
				}
				removeBeforeWrite()
				clog.Infof(ctx, "flush %s %s clone from source %s", fname, d, lsrc.Fname)
				err := osfsc.Clonefile(ctx, lsrc.Fname, fname)
				if err == nil {
					err = osfs.Chmod(ctx, fname, e.mode)
					return err
				}
				// clonefile err, fallback to normal copy
				clog.Warningf(ctx, "clonefile failed: %v", err)
			}
			var srcname string
			if ok {
				srcname = lsrc.Fname
			}
			// write into tmp and rename after remove.
			// e.src may be the same as fname, but
			// we may need to remove fname for some reason
			// (hardlink etc).
			tmpname := filepath.Join(filepath.Dir(fname), "."+filepath.Base(fname)+".tmp")
			ctx, cancel := digest.ContextWithTimeout(ctx, e.d)
			defer cancel()
			err := retry.Do(ctx, func() error {
				return osfs.WriteDigestData(ctx, tmpname, e.src, e.mode, timeout)
			})
			if err != nil {
				return fmt.Errorf("flush tmp %s size=%d %s: %w", tmpname, d.SizeBytes, time.Since(started), err)
			}
			removeBeforeWrite()
			clog.Infof(ctx, "flush %s %s from source %s", fname, d, srcname)
			err = osfs.Rename(ctx, tmpname, fname)
			return err
		}()
	} else {
		removeBeforeWrite()
		clog.Infof(ctx, "flush %s from embedded buf", fname)
		err = osfs.WriteFile(ctx, fname, buf, e.mode)
	}
	if err != nil {
		return err
	}
	err = osfs.Chtimes(ctx, fname, time.Time{}, mtime)
	if err != nil {
		return err
	}
	return nil
}
