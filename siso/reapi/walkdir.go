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

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/o11y/trace"
	"go.chromium.org/build/siso/reapi/retry"
)

// WalkDir walks the directory tree rooted identified by d,
// calling fn for each directory in the tree, including root.
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
			for dir, err := range c.walkDirIter(ctx, d, &stats) {
				if err != nil {
					return err
				}
				dd, derr := blob.FromProtoMessage(c.digestFn, dir)
				if derr != nil {
					return derr
				}
				if known[dd.Digest()] != nil {
					clog.Warningf(ctx, "duplicate dir %s", dd.Digest())
					continue
				}
				known[dd.Digest()] = dir
				err := handleDir(dd.Digest(), dir)
				if err != nil {
					return err
				}
			}
			break
		}
		return nil
	})
	clog.Infof(ctx, "walkdir %s %s", d, stats.String())
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

func (c *Client) walkDirIter(ctx context.Context, d digest.Digest, stats *walkDirStats) iter.Seq2[*rpb.Directory, error] {
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

func (c *Client) cachedTreeIter(d digest.Digest, stats *walkDirStats) (iter.Seq2[*rpb.Directory, error], error) {
	var dirs []*rpb.Directory
	pendings := []digest.Digest{d}
	seen := make(map[digest.Digest]struct{})
	seen[d] = struct{}{}
	for len(pendings) > 0 {
		d := pendings[0]
		pendings = pendings[1:]
		data, ok := c.walkdirCache.Load(d)
		if !ok {
			return nil, fmt.Errorf("no directory for %s", d)
		}
		stats.nhit++
		dir := &rpb.Directory{}
		err := proto.Unmarshal(data.([]byte), dir)
		if err != nil {
			return nil, fmt.Errorf("unmarshal directory for %s: %w", d, err)
		}
		dirs = append(dirs, dir)
		for _, subdir := range dir.Directories {
			sd := digest.FromProto(subdir.Digest)
			if _, ok := seen[sd]; ok {
				continue
			}
			pendings = append(pendings, sd)
			seen[sd] = struct{}{}
		}
	}
	return func(yield func(*rpb.Directory, error) bool) {
		for _, dir := range dirs {
			if !yield(dir, nil) {
				return
			}
		}
	}, nil
}

func (c *Client) getTreeIter(ctx context.Context, d digest.Digest, stats *walkDirStats) iter.Seq2[*rpb.Directory, error] {
	casClient := rpb.NewContentAddressableStorageClient(c.casConn)
	return func(yield func(*rpb.Directory, error) bool) {
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
			yield(nil, err)
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
				yield(nil, err)
				return
			}
			dirs := resp.Directories
			for len(dirs) > 0 {
				dir := dirs[0]
				dirs = dirs[1:]
				dd, err := blob.FromProtoMessage(c.digestFn, dir)
				if err != nil {
					yield(dir, err)
					return
				}
				data, err := proto.Marshal(dir)
				if err != nil {
					yield(dir, err)
					return
				}
				if _, ok := seen[dd.Digest()]; !ok {
					stats.ncached++
					c.walkdirCache.Store(dd.Digest(), data)
					seen[dd.Digest()] = struct{}{}
				}
				delete(waits, dd.Digest())
				if !yield(dir, nil) {
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
					data, ok := c.walkdirCache.Load(sd)
					if ok {
						subdir := &rpb.Directory{}
						err := proto.Unmarshal(data.([]byte), subdir)
						if err != nil {
							yield(nil, err)
							return
						}
						stats.nhit++
						seen[sd] = struct{}{}
						dirs = append(dirs, subdir)
						continue
					}
					waits[sd] = struct{}{}
				}

			}
			stats.pageToken = resp.NextPageToken
		}
	}
}

func (c *Client) readDirIter(ctx context.Context, d digest.Digest, stats *walkDirStats) iter.Seq2[*rpb.Directory, error] {
	casClient := rpb.NewContentAddressableStorageClient(c.casConn)
	return func(yield func(*rpb.Directory, error) bool) {
		pendings := []digest.Digest{d}
		seen := make(map[digest.Digest]struct{})
		seen[d] = struct{}{}
		for len(pendings) > 0 {
			req := &rpb.BatchReadBlobsRequest{
				InstanceName:   c.opt.Instance,
				DigestFunction: c.digestFn.Value(),
			}
			responses := make([]*rpb.BatchReadBlobsResponse_Response, 0, len(pendings))
			for _, d := range pendings {
				data, ok := c.walkdirCache.Load(d)
				if ok {
					stats.nhit++
					responses = append(responses, &rpb.BatchReadBlobsResponse_Response{
						Digest: d.Proto(),
						Data:   data.([]byte),
						// compressor identity
					})
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
					yield(nil, err)
					return
				}
			}
			for _, rd := range append(responses, resp.GetResponses()...) {
				dd := digest.FromProto(rd.Digest)
				if rd.Status.GetCode() != 0 {
					yield(nil, fmt.Errorf("blob %s resp: %w", dd, status.FromProto(rd.Status).Err()))
					return
				}
				data, err := c.decodeForBatchRead(dd, rd.Data, rd.Compressor)
				if err != nil {
					yield(nil, fmt.Errorf("blob %s decode: %w", dd, err))
					return
				}
				dir := &rpb.Directory{}
				err = proto.Unmarshal(data, dir)
				if err != nil {
					yield(nil, fmt.Errorf("blob %s unmarshal: %w", dd, err))
					return
				}
				stats.ncached++
				c.walkdirCache.Store(dd, data)
				if !yield(dir, nil) {
					return
				}
				for _, subdir := range dir.Directories {
					sd := digest.FromProto(subdir.Digest)
					if _, ok := seen[sd]; ok {
						continue
					}
					pendings = append(pendings, sd)
					seen[sd] = struct{}{}
				}
			}
		}
	}
}
