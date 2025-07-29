// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package scandeps

import (
	"context"
	"errors"
	"fmt"
	"hash/maphash"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	log "github.com/golang/glog"

	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/o11y/clog"
)

const maxSymlinks = 40

// filesystem is mirror of hashfs to optimize for scandeps access pattern.
// it is shared for all scandeps processes.
// without this, hashfs would have lots of negative caches for non-existing
// header files for every include directory.
type filesystem struct {
	hashfs *hashfs.HashFS

	// shard by basename to reduce lock contention
	dirs  sync.Map // basename -> dir -> []dirents
	files sync.Map // basename -> files -> *scanresult

	dircache sync.Map // dir -> base -> bool

	hmaps sync.Map // hmap path -> *hmapresult

	// shard by maphash to reduce lock contention
	symtab  [256]sync.Map  // for incname, macros
	pathtab [4096]sync.Map // for pathname
	seed    maphash.Seed
}

type dircache struct {
	ready          chan struct{}
	m              sync.Map
	symlinkTargets []string
	err            error
}

// update updates filesystem modification by fi.
func (fsys *filesystem) update(ctx context.Context, fi *hashfs.FileInfo) {
	if log.V(1) {
		clog.Infof(ctx, "update %s dir:%t", fi.Path(), fi.IsDir())
	}
	var dname string
	var base string
	if !fi.IsDir() {
		fname := filepath.ToSlash(fi.Path())
		fsys.forgetFile(fname)
		base = filepath.Base(fname)
		dname = filepath.ToSlash(filepath.Dir(fname))
	} else {
		dname = filepath.ToSlash(fi.Path())
	}
	// fix dircache
	for dname := dname; !strings.HasSuffix(dname, "/"); {
		v, ok := fsys.dircache.Load(dname)
		if !ok {
			base = filepath.Base(dname)
			dname = filepath.ToSlash(filepath.Dir(dname))
			continue
		}
		dc := v.(*dircache)
		select {
		case <-dc.ready:
		default:
			clog.Infof(ctx, "update race ReadDir&update %s", fi.Path())
			fsys.dircache.Delete(dname)
			base = filepath.Base(dname)
			dname = filepath.ToSlash(filepath.Dir(dname))
			continue
		}
		if dc.err != nil {
			// negative cache?
			clog.Infof(ctx, "update clear negative cache %s %v", fi.Path(), dc.err)
			fsys.dircache.Delete(dname)
			base = filepath.Base(dname)
			dname = filepath.ToSlash(filepath.Dir(dname))
			continue
		}
		if base != "" {
			dc.m.LoadOrStore(base, true)
		}
		base = filepath.Base(dname)
		dname = filepath.ToSlash(filepath.Dir(dname))
	}
	for !strings.HasSuffix(dname, "/") {
		if fsys.markDirExists(dname) {
			return
		}
		dname = filepath.ToSlash(filepath.Dir(dname))
	}
}

func (fsys *filesystem) forgetFile(fname string) {
	base := filepath.Base(fname)
	v, ok := fsys.files.Load(base)
	if ok {
		m := v.(*sync.Map)
		m.Delete(fname)
	}
}

func (fsys *filesystem) markDirExists(dname string) bool {
	v, _ := fsys.dirs.LoadOrStore(filepath.Base(dname), new(sync.Map))
	m := v.(*sync.Map)
	v, ok := m.Load(dname)
	if !ok {
		m.Store(dname, true)
		return false
	}
	exist := v.(bool)
	if exist {
		return true
	}
	m.Store(dname, true)
	return false
}

func (fsys *filesystem) ReadDir(ctx context.Context, execRoot, dname string) (*sync.Map, []string, error) {
	fullpath := filepath.ToSlash(filepath.Join(execRoot, dname))
	dv, loaded := fsys.dircache.LoadOrStore(fullpath, &dircache{
		ready: make(chan struct{}),
	})
	dc := dv.(*dircache)
	if !loaded {
		go func() {
			if log.V(1) {
				clog.Infof(ctx, "fsys readdir %s", dname)
			}
			symlinkErr := fmt.Errorf("readdir %s: %w", dname, syscall.ELOOP)
			var dents []hashfs.DirEntry
			var visited []string
			var err error
			for range maxSymlinks {
				if filepath.IsAbs(dname) {
					execRoot = ""
				}
				dents, err = fsys.hashfs.ReadDir(ctx, execRoot, dname)
				if err == nil {
					break
				}
				var errSymlink hashfs.SymlinkError
				if !errors.As(err, &errSymlink) {
					break
				}
				clog.Infof(ctx, "readdir symlink %#v", errSymlink)
				target := errSymlink.Target
				if !filepath.IsAbs(target) {
					target = filepath.ToSlash(filepath.Join(filepath.Dir(errSymlink.Path), target))
				}
				// target may escape exec root.
				if !filepath.IsLocal(target) {
					target = filepath.ToSlash(filepath.Join(execRoot, target))
				}
				clog.Infof(ctx, "symlink dir: %s -> %s", dname, target)
				dname = target
				if filepath.IsLocal(dname) {
					visited = append(visited, dname)
				}
				clog.Infof(ctx, "retry symlnk %s", dname)
				err = symlinkErr
			}
			dc.err = err
			dc.symlinkTargets = visited
			for _, de := range dents {
				if log.V(1) {
					clog.Infof(ctx, "dirent %q %q", fullpath, de.Name())
				}
				dc.m.Store(fsys.pathIntern(de.Name()), true)
			}
			dname = fullpath
			for !strings.HasSuffix(dname, "/") {
				if fsys.markDirExists(dname) {
					break
				}
				dname = filepath.ToSlash(filepath.Dir(dname))
			}
			close(dc.ready)
		}()
	}
	select {
	case <-ctx.Done():
		return nil, nil, fmt.Errorf("readdirnames[wait]: %w", context.Cause(ctx))
	case <-dc.ready:
	}
	if dc.err != nil {
		return nil, dc.symlinkTargets, dc.err
	}
	return &dc.m, dc.symlinkTargets, nil
}

func (fsys *filesystem) intern(v string) string {
	v = strings.Clone(v)
	i := int(maphash.String(fsys.seed, v) % uint64(len(fsys.symtab)))
	vv, _ := fsys.symtab[i].LoadOrStore(v, v)
	return vv.(string)
}

func (fsys *filesystem) pathIntern(v string) string {
	i := int(maphash.String(fsys.seed, v) % uint64(len(fsys.pathtab)))
	vv, _ := fsys.pathtab[i].LoadOrStore(v, v)
	return vv.(string)
}

func (fsys *filesystem) getDir(execRoot, dname string) (exist, ok bool) {
	base := filepath.Base(dname)
	v, _ := fsys.dirs.LoadOrStore(base, new(sync.Map))
	m := v.(*sync.Map)
	v, ok = m.Load(filepath.ToSlash(filepath.Join(execRoot, dname)))
	if !ok {
		return false, false
	}
	exist = v.(bool)
	return exist, true
}

func (fsys *filesystem) setDir(execRoot, dname string, exist bool) {
	v, _ := fsys.dirs.LoadOrStore(filepath.Base(dname), new(sync.Map))
	m := v.(*sync.Map)
	m.Store(filepath.ToSlash(filepath.Join(execRoot, dname)), exist)
}

func (fsys *filesystem) getFile(execRoot, fname string) (*scanResult, bool) {
	v, ok := fsys.files.Load(filepath.Base(fname))
	if !ok {
		return nil, false
	}
	m := v.(*sync.Map)
	v, ok = m.Load(filepath.ToSlash(filepath.Join(execRoot, fname)))
	if !ok {
		return nil, false
	}
	sr := v.(*scanResult)
	return sr, true
}

func (fsys *filesystem) setFile(execRoot, fname string, sr *scanResult) {
	v, _ := fsys.files.LoadOrStore(filepath.Base(fname), new(sync.Map))
	m := v.(*sync.Map)
	m.Store(filepath.ToSlash(filepath.Join(execRoot, fname)), sr)
}

type hmapresult struct {
	mu sync.Mutex
	// done indicates if it has already been computed or not.
	done bool
	// ok indicates if the hmap was successfully parsed or not.
	ok bool
	// hmap entries: include -> file path.
	m map[string]string
}

// getHmap returns hmap and success flag.
// If the same hamp has been computed, the results are returned from cache.
func (fsys *filesystem) getHmap(ctx context.Context, execRoot, fname string) (map[string]string, bool) {
	clog.Infof(ctx, "check hmap %s", fname)
	v, _ := fsys.hmaps.LoadOrStore(filepath.ToSlash(filepath.Join(execRoot, fname)), new(hmapresult))
	hr := v.(*hmapresult)
	hr.mu.Lock()
	defer hr.mu.Unlock()
	defer func() { hr.done = true }()
	if hr.done {
		clog.Infof(ctx, "check hmap %s: reuse ok=%t", fname, hr.ok)
		return hr.m, hr.ok
	}
	buf, err := fsys.hashfs.ReadFile(ctx, execRoot, fname)
	if err != nil {
		clog.Warningf(ctx, "missing hmap %s: %v", fname, err)
		return nil, false
	}
	m, err := ParseHeaderMap(ctx, buf)
	if err != nil {
		clog.Warningf(ctx, "failed to parse hmap %s: %v", fname, err)
		return nil, false
	}
	clog.Infof(ctx, "hmap %s %d => %v", fname, len(buf), m)
	hr.m = m
	hr.ok = true
	return hr.m, hr.ok
}

func (fsys *filesystem) readFile(ctx context.Context, root, fname string) ([]byte, []string, error) {
	reqname := fname
	var visited []string
	execRoot := root
	for range maxSymlinks {
		if filepath.IsAbs(fname) {
			execRoot = ""
		}
		buf, err := fsys.hashfs.ReadFile(ctx, execRoot, fname)
		var errSymlink hashfs.SymlinkError
		if err != nil && !errors.As(err, &errSymlink) {
			return nil, visited, err
		}
		if err == nil {
			return buf, visited, nil
		}
		clog.Infof(ctx, "readfile symlink %#v", errSymlink)
		target := errSymlink.Target
		if !filepath.IsAbs(target) {
			target = filepath.ToSlash(filepath.Join(filepath.ToSlash(filepath.Dir(errSymlink.Path)), target))
		}
		if !filepath.IsLocal(target) {
			target = filepath.ToSlash(filepath.Join(execRoot, target))
		}
		fname = target
		if !filepath.IsAbs(fname) {
			visited = append(visited, fname)
		}
		clog.Infof(ctx, "retry symlnk %s", fname)
	}
	return nil, visited, fmt.Errorf("read %q: %w", reqname, syscall.ELOOP)

}

func (fsys *filesystem) statFollowSymlink(ctx context.Context, root, fname string) (hashfs.FileInfo, error) {
	reqname := fname
	execRoot := root
	for range maxSymlinks {
		if filepath.IsAbs(fname) {
			execRoot = ""
		}
		fi, err := fsys.hashfs.Stat(ctx, execRoot, fname)
		if err != nil {
			return fi, err
		}
		if fi.Target() == "" {
			return fi, nil
		}
		target := fi.Target()
		if !filepath.IsAbs(target) {
			orig := fi.Path()
			relOrig, err := filepath.Rel(root, orig)
			if err != nil || !filepath.IsLocal(relOrig) {
				target = filepath.ToSlash(filepath.Join(filepath.Dir(orig), target))
			} else {
				target = filepath.ToSlash(filepath.Join(filepath.Dir(relOrig), target))
			}
		}
		if !filepath.IsLocal(target) {
			target = filepath.ToSlash(filepath.Join(execRoot, target))
		}
		clog.Infof(ctx, "stat follow %s -> %s", fname, target)
		fname = target
	}
	return hashfs.FileInfo{}, fmt.Errorf("stat %q: %w", reqname, syscall.ELOOP)
}
