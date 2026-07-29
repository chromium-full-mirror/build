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
							return nil
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
	return fmt.Sprintf("api %d %s avg:%s dir %d cb %s avg:%s",
		s.nrecvs, s.apiDur, avgAPIDur, s.ndirs, s.cbDur, avgCBDur)
}

func (c *Client) walkDirIter(ctx context.Context, d digest.Digest, stats *walkDirStats) iter.Seq2[*rpb.Directory, error] {
	if c.opt.WalkDirStream {
		return c.getTreeIter(ctx, d, stats)
	}
	return c.readDirIter(ctx, d, stats)
}

func (c *Client) getTreeIter(ctx context.Context, d digest.Digest, stats *walkDirStats) iter.Seq2[*rpb.Directory, error] {
	casClient := rpb.NewContentAddressableStorageClient(c.casConn)
	return func(yield func(*rpb.Directory, error) bool) {
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
		for {
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
			for _, dir := range resp.Directories {
				if !yield(dir, nil) {
					return
				}
			}
			stats.pageToken = resp.NextPageToken
		}
	}
}

func (c *Client) readDirIter(ctx context.Context, d digest.Digest, stats *walkDirStats) iter.Seq2[*rpb.Directory, error] {
	casClient := rpb.NewContentAddressableStorageClient(c.casConn)
	var compressors []rpb.Compressor_Value
	if c.opt.BatchCompressedBlob > 0 {
		compressors = []rpb.Compressor_Value{
			rpb.Compressor_ZSTD,
			rpb.Compressor_DEFLATE,
		}
	}
	return func(yield func(*rpb.Directory, error) bool) {
		pendings := []digest.Digest{d}
		seen := make(map[digest.Digest]struct{})
		seen[d] = struct{}{}
		for len(pendings) > 0 {
			req := &rpb.BatchReadBlobsRequest{
				InstanceName:          c.opt.Instance,
				AcceptableCompressors: compressors,
				DigestFunction:        c.digestFn.Value(),
			}
			for _, d := range pendings {
				req.Digests = append(req.Digests, d.Proto())
			}
			pendings = pendings[:0]
			started := time.Now()
			resp, err := casClient.BatchReadBlobs(ctx, req)
			dur := time.Since(started)
			stats.apiDur += dur
			stats.nrecvs++
			if err != nil {
				yield(nil, err)
				return
			}
			for _, rd := range resp.Responses {
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
