// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package reapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"path"
	"time"

	log "github.com/golang/glog"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/o11y/trace"
	"go.chromium.org/build/siso/reapi/retry"
)

// walkDirEntry is a directory produced by a walkDirIter iterator.
// d is the digest of dir: the digest it was looked up by in walkdirCache or
// fetched by from CAS, or, for a directory streamed by GetTree, the digest
// of its serialization.
type walkDirEntry struct {
	d   digest.Digest
	dir *rpb.Directory
}

// loadWalkdir returns the directory d from walkdirCache.
func (c *Client) loadWalkdir(d digest.Digest) (*rpb.Directory, bool) {
	v, ok := c.walkdirCache.Load(d)
	if !ok {
		return nil, false
	}
	return v.(*rpb.Directory), true
}

// WalkDir walks the directory tree rooted identified by d,
// calling fn for each directory in the tree, including root.
// dir is shared with other walks and must not be modified.
func (c *Client) WalkDir(ctx context.Context, d digest.Digest, fn func(dname string, dir *rpb.Directory) error) error {
	if c == nil {
		return fmt.Errorf("reapi is not configured")
	}
	ctx, span := trace.NewSpan(ctx, "reapi-walkdir")
	defer span.Close(nil)

	dirs := make(map[string]digest.Digest)      // dname -> digest
	waiting := make(map[digest.Digest][]string) // digest -> dnames
	waiting[d] = append(waiting[d], "")
	known := make(map[digest.Digest]*rpb.Directory)
	var stats walkDirStats
	err := retry.Do(ctx, func() error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		for {
			var handleDir func(d digest.Digest, dir *rpb.Directory) error
			handleDir = func(d digest.Digest, dir *rpb.Directory) error {
				if len(waiting[d]) == 0 {
					return nil
				}
				var dnames []string
				dnames, waiting[d] = waiting[d], nil
				for _, dname := range dnames {
					dirs[dname] = d
					started := time.Now()
					err := fn(dname, dir)
					stats.cbDur += time.Since(started)
					stats.ndirs++
					if err != nil {
						return err
					}
					for _, subdir := range dir.Directories {
						sd := digest.FromProto(subdir.Digest)
						subname := path.Join(dname, subdir.Name)
						waiting[sd] = append(waiting[sd], subname)
						if sdir := known[sd]; sdir != nil {
							// check loop
							for pname := dname; pname != "" && pname != "."; pname = path.Dir(pname) {
								if dirs[pname] == sd {
									return fmt.Errorf("directory loop %s from %s -> %s", sd, subname, pname)
								}
							}
							err := handleDir(sd, sdir)
							if err != nil {
								return err
							}
						}
					}
				}
				return nil
			}
			for ent, err := range c.walkDirIter(ctx, d, &stats) {
				if err != nil {
					return err
				}
				dd, dir := ent.d, ent.dir
				if known[dd] != nil {
					// WalkDir already handled this directory.
					if log.V(1) {
						clog.Infof(ctx, "duplicate dir %s", dd)
					}
					continue
				}
				known[dd] = dir
				err := handleDir(dd, dir)
				if err != nil {
					return err
				}
			}
			break
		}
		return nil
	})
	if log.V(1) {
		clog.Infof(ctx, "walkdir %s %s", d, stats.String())
	}
	return err
}

type walkDirStats struct {
	pageToken     string
	apiDur, cbDur time.Duration
	nrecvs, ndirs int
	ncached       int
	nhit          int
}

func (s *walkDirStats) String() string {
	var avgAPIDur time.Duration
	if s.nrecvs > 0 {
		avgAPIDur = s.apiDur / time.Duration(s.nrecvs)
	}
	var avgCBDur time.Duration
	if s.ndirs > 0 {
		avgCBDur = s.cbDur / time.Duration(s.ndirs)
	}
	return fmt.Sprintf("api %d %s avg:%s dir %d cb %s avg:%s cached %d hit %d",
		s.nrecvs, s.apiDur, avgAPIDur, s.ndirs, s.cbDur, avgCBDur, s.ncached, s.nhit)
}

func (c *Client) walkDirIter(ctx context.Context, d digest.Digest, stats *walkDirStats) iter.Seq2[walkDirEntry, error] {
	_, ok := c.walkdirCache.Load(d)
	if ok {
		iter, err := c.cachedTreeIter(d, stats)
		if err == nil {
			return iter
		}
	}
	if c.opt.WalkDirStream {
		return c.getTreeIter(ctx, d, stats)
	}
	return c.readDirIter(ctx, d, stats)
}

func (c *Client) cachedTreeIter(d digest.Digest, stats *walkDirStats) (iter.Seq2[walkDirEntry, error], error) {
	var dirs []walkDirEntry
	pendings := []digest.Digest{d}
	seen := make(map[digest.Digest]struct{})
	seen[d] = struct{}{}
	for len(pendings) > 0 {
		d := pendings[0]
		pendings = pendings[1:]
		dir, ok := c.loadWalkdir(d)
		if !ok {
			return nil, fmt.Errorf("no directory for %s", d)
		}
		stats.nhit++
		dirs = append(dirs, walkDirEntry{d: d, dir: dir})
		for _, subdir := range dir.Directories {
			sd := digest.FromProto(subdir.Digest)
			if _, ok := seen[sd]; ok {
				continue
			}
			pendings = append(pendings, sd)
			seen[sd] = struct{}{}
		}
	}
	return func(yield func(walkDirEntry, error) bool) {
		for _, ent := range dirs {
			if !yield(ent, nil) {
				return
			}
		}
	}, nil
}

func (c *Client) getTreeIter(ctx context.Context, d digest.Digest, stats *walkDirStats) iter.Seq2[walkDirEntry, error] {
	casClient := rpb.NewContentAddressableStorageClient(c.casConn)
	return func(yield func(walkDirEntry, error) bool) {
		seen := make(map[digest.Digest]struct{})
		waits := make(map[digest.Digest]struct{})
		waits[d] = struct{}{}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stream, err := casClient.GetTree(ctx, &rpb.GetTreeRequest{
			InstanceName:   c.opt.Instance,
			RootDigest:     d.Proto(),
			PageToken:      stats.pageToken,
			DigestFunction: c.digestFn.Value(),
		})
		if err != nil {
			yield(walkDirEntry{}, err)
			return
		}
		for len(waits) > 0 {
			started := time.Now()
			resp, err := stream.Recv()
			dur := time.Since(started)
			stats.apiDur += dur
			stats.nrecvs++
			if log.V(1) {
				clog.Infof(ctx, "recv %d %s", len(resp.GetDirectories()), err)
			}
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				yield(walkDirEntry{}, err)
				return
			}
			// Directories streamed by GetTree carry no digest, so
			// compute it (and store them in walkdirCache). Those
			// taken from walkdirCache below keep their cache key.
			dirs := make([]walkDirEntry, 0, len(resp.Directories))
			for _, dir := range resp.Directories {
				dirs = append(dirs, walkDirEntry{dir: dir})
			}
			for len(dirs) > 0 {
				dir := dirs[0].dir
				dd := dirs[0].d
				dirs = dirs[1:]
				if dd.IsZero() {
					data, err := proto.Marshal(dir)
					if err != nil {
						yield(walkDirEntry{dir: dir}, err)
						return
					}
					dd = c.digestFn.FromBytes(data)
					if _, ok := seen[dd]; !ok {
						stats.ncached++
						c.walkdirCache.Store(dd, dir)
						seen[dd] = struct{}{}
					}
				}
				delete(waits, dd)
				if !yield(walkDirEntry{d: dd, dir: dir}, nil) {
					return
				}
				for _, sdn := range dir.Directories {
					sd := digest.FromProto(sdn.GetDigest())
					if _, ok := waits[sd]; ok {
						stats.nhit++
						continue
					}
					if _, ok := seen[sd]; ok {
						stats.nhit++
						continue
					}
					subdir, ok := c.loadWalkdir(sd)
					if ok {
						stats.nhit++
						seen[sd] = struct{}{}
						dirs = append(dirs, walkDirEntry{d: sd, dir: subdir})
						continue
					}
					waits[sd] = struct{}{}
				}

			}
			stats.pageToken = resp.NextPageToken
		}
	}
}

func (c *Client) readDirIter(ctx context.Context, d digest.Digest, stats *walkDirStats) iter.Seq2[walkDirEntry, error] {
	casClient := rpb.NewContentAddressableStorageClient(c.casConn)
	return func(yield func(walkDirEntry, error) bool) {
		pendings := []digest.Digest{d}
		seen := make(map[digest.Digest]struct{})
		seen[d] = struct{}{}
		visit := func(ent walkDirEntry) bool {
			if !yield(ent, nil) {
				return false
			}
			for _, subdir := range ent.dir.Directories {
				sd := digest.FromProto(subdir.Digest)
				if _, ok := seen[sd]; ok {
					continue
				}
				pendings = append(pendings, sd)
				seen[sd] = struct{}{}
			}
			return true
		}
		for len(pendings) > 0 {
			req := &rpb.BatchReadBlobsRequest{
				InstanceName:   c.opt.Instance,
				DigestFunction: c.digestFn.Value(),
			}
			var cached []walkDirEntry
			for _, d := range pendings {
				dir, ok := c.loadWalkdir(d)
				if ok {
					stats.nhit++
					cached = append(cached, walkDirEntry{d: d, dir: dir})
					continue
				}
				req.Digests = append(req.Digests, d.Proto())
			}
			pendings = pendings[:0]
			var resp *rpb.BatchReadBlobsResponse
			if len(req.Digests) > 0 {
				started := time.Now()
				var err error
				resp, err = casClient.BatchReadBlobs(ctx, req)
				dur := time.Since(started)
				stats.apiDur += dur
				stats.nrecvs++
				if err != nil {
					yield(walkDirEntry{}, err)
					return
				}
			}
			for _, ent := range cached {
				if !visit(ent) {
					return
				}
			}
			for _, rd := range resp.GetResponses() {
				dd := digest.FromProto(rd.Digest)
				if rd.Status.GetCode() != 0 {
					yield(walkDirEntry{}, fmt.Errorf("blob %s resp: %w", dd, status.FromProto(rd.Status).Err()))
					return
				}
				data, err := c.decodeForBatchRead(dd, rd.Data, rd.Compressor)
				if err != nil {
					yield(walkDirEntry{}, fmt.Errorf("blob %s decode: %w", dd, err))
					return
				}
				// WalkDir and walkdirCache use dd as the digest of
				// data, so check it.
				if got := c.digestFn.FromBytes(data); got != dd {
					yield(walkDirEntry{}, fmt.Errorf("blob %s digest mismatch: got %s", dd, got))
					return
				}
				dir := &rpb.Directory{}
				err = proto.Unmarshal(data, dir)
				if err != nil {
					yield(walkDirEntry{}, fmt.Errorf("blob %s unmarshal: %w", dd, err))
					return
				}
				stats.ncached++
				c.walkdirCache.Store(dd, dir)
				if !visit(walkDirEntry{d: dd, dir: dir}) {
					return
				}
			}
		}
	}
}
