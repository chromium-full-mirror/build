// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package reapi

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"iter"
	"slices"

	bpb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/reapi/bytestreamio"
)

func (c *Client) chunkReader(ctx context.Context, d digest.Digest, fname string) (*chunkReader, error) {
	if c.opt.LocalCache == nil {
		return nil, errors.New("chunk reader disabled: no local cache")
	}
	casClient := rpb.NewContentAddressableStorageClient(c.casConn)
	resp, err := casClient.SplitBlob(ctx, &rpb.SplitBlobRequest{
		InstanceName:   c.Instance(),
		BlobDigest:     d.Proto(),
		DigestFunction: c.digestFn.Value(),
		// TODO: select appropriate chunking function.
		ChunkingFunction: rpb.ChunkingFunction_FAST_CDC_2020,
	})
	if err != nil {
		return nil, err
	}
	// retrieve chunks in local cache.
	for batch := range c.chunkReadBatch(ctx, resp.ChunkDigests) {
		// TODO: concurrent fetch chunk?
		err := c.fetchChunks(ctx, batch, fname)
		if err != nil {
			return nil, err
		}
	}
	var chunks []chunk
	var offset int64
	for _, dp := range resp.ChunkDigests {
		d := digest.FromProto(dp)
		chunks = append(chunks, chunk{
			d:      d,
			offset: offset,
		})
		offset += d.SizeBytes
	}
	return &chunkReader{
		c:      c,
		ctx:    ctx,
		fname:  fname,
		d:      d,
		hasher: c.digestFn.NewContentHasher(d.SizeBytes),
		chunks: chunks,
	}, nil
}

// chunkReader is a io.ReadCloser for d using chunks.
type chunkReader struct {
	c      *Client
	ctx    context.Context
	fname  string
	d      digest.Digest
	rd     io.ReadCloser
	hasher hash.Hash
	size   int64
	chunks []chunk
}

func (r *chunkReader) Read(buf []byte) (int, error) {
	for {
		if r.rd == nil {
			if len(r.chunks) == 0 {
				// EOF. verify digest.
				rd := digest.Digest{
					Hash:      hex.EncodeToString(r.hasher.Sum(nil)),
					SizeBytes: r.size,
				}
				if r.d != rd {
					return 0, fmt.Errorf("chunked blob digest mismatch: request=%s got=%s", r.d, rd)
				}
				return 0, io.EOF
			}
			chunk := r.chunks[0]
			r.chunks = r.chunks[1:]
			src := r.c.opt.LocalCache.Source(r.ctx, chunk.d, chunk.Name(r.fname))
			data := blob.NewData(src, chunk.d)
			rd, err := data.Open(r.ctx)
			if err != nil {
				return 0, err
			}
			rr := r.rd
			r.rd = rd
			if rr != nil {
				err = rr.Close()
				if err != nil {
					return 0, err
				}
			}
		}
		n, err := r.rd.Read(buf)
		if n > 0 {
			r.hasher.Write(buf[:n]) // hash.Hash never return error
			r.size += int64(n)
			switch {
			case r.size > r.d.SizeBytes:
				err = fmt.Errorf("chunked blob size mismatch: requested=%d got=%d", r.d.SizeBytes, r.size)
			case r.size == r.d.SizeBytes:
				eoferr := expectEOF(r.rd)
				if eoferr != nil || len(r.chunks) > 0 {
					err = fmt.Errorf("chunked blob has more data size=%d remaining chunks=%d: %w", r.size, len(r.chunks), eoferr)
				} else {
					// EOF. verify digest
					rd := digest.Digest{
						Hash:      hex.EncodeToString(r.hasher.Sum(nil)),
						SizeBytes: r.size,
					}
					if rd != r.d {
						err = fmt.Errorf("chunked blob digest mismatch: requested=%s got=%s", r.d, rd)
					}
				}
			}
		}
		if errors.Is(err, io.EOF) {
			rr := r.rd
			r.rd = nil
			err = rr.Close()
			if err != nil {
				return 0, err
			}
			if n == 0 {
				// try next chunk, rather than return 0, nil
				continue
			}
		}
		return n, err
	}
}

func (r *chunkReader) Close() error {
	r.chunks = nil
	rr := r.rd
	r.rd = nil
	if rr != nil {
		err := rr.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

type chunk struct {
	d      digest.Digest
	offset int64
}

func (c chunk) Name(fname string) string {
	return fmt.Sprintf("%s#%d+%d", fname, c.offset, c.d.SizeBytes)
}

func (c *Client) chunkReadBatch(ctx context.Context, chunkDigests []*rpb.Digest) iter.Seq[[]chunk] {
	c.mu.Lock()
	chunkBatchLimit := c.opt.chunkBatchThreshold
	c.mu.Unlock()
	return func(yield func([]chunk) bool) {
		var batch []chunk
		var size int64
		var offset int64
		for _, dp := range chunkDigests {
			d := digest.FromProto(dp)
			if c.opt.LocalCache.HasContent(ctx, d) {
				offset += d.SizeBytes
				continue
			}
			batch = append(batch, chunk{
				d:      d,
				offset: offset,
			})
			size += d.SizeBytes
			if size < chunkBatchLimit {
				continue
			}
			// exceed chunkBatchThreshold
			var nextBatch []chunk
			if len(batch) > 1 {
				// exceed batch.
				// last chunk in next batch
				last := batch[len(batch)-1]
				batch = batch[:len(batch)-1]
				nextBatch = []chunk{last}
				size = d.SizeBytes
			} else {
				// exceed batch, but only one chunk in batch.
				// use it with bytestream
				size = 0
			}
			if !yield(batch) {
				return
			}
			batch = nextBatch
		}
		if len(batch) > 0 {
			yield(batch)
		}
	}
}

func (c *Client) fetchChunks(ctx context.Context, chunks []chunk, fname string) error {
	// TODO: singleflight per chunk digest?

	if len(chunks) == 1 && chunks[0].d.SizeBytes >= c.opt.ByteStreamReadThreshold {
		if c.opt.LocalCache.HasContent(ctx, chunks[0].d) {
			// already exist in local cache.
			return nil
		}
		// large chunk that not fit for BatchReadBlobs.
		r, err := bytestreamio.Open(ctx, bpb.NewByteStreamClient(c.casDataConn), c.resourceName(chunks[0].d))
		if err != nil {
			return err
		}
		rd, err := c.newDecoder(r, chunks[0].d)
		if err != nil {
			return err
		}
		defer rd.Close()
		wr, err := c.opt.LocalCache.ContentSink(ctx, chunks[0].d, chunks[0].Name(fname))
		if err != nil {
			return err
		}
		_, err = io.Copy(wr, rd)
		cerr := wr.Close()
		if err == nil {
			err = cerr
		}
		return err
	}
	casClient := rpb.NewContentAddressableStorageClient(c.casConn)
	var acceptableCompressors []rpb.Compressor_Value
	if c.opt.BatchCompressedBlob > 0 {
		acceptableCompressors = []rpb.Compressor_Value{
			rpb.Compressor_ZSTD,
			rpb.Compressor_DEFLATE,
		}
	}
	req := &rpb.BatchReadBlobsRequest{
		InstanceName:          c.opt.Instance,
		AcceptableCompressors: acceptableCompressors,
		DigestFunction:        c.digestFn.Value(),
	}
	dnames := make(map[digest.Digest]string)
	for _, chunk := range chunks {
		dnames[chunk.d] = chunk.Name(fname)
		if c.opt.LocalCache.HasContent(ctx, chunk.d) {
			// already exist in local cache.
			continue
		}
		req.Digests = append(req.Digests, chunk.d.Proto())
	}
	for len(req.Digests) > 0 {
		resp, err := casClient.BatchReadBlobs(ctx, req)
		if err != nil {
			return err
		}
		if len(resp.Responses) == 0 {
			return fmt.Errorf("no response in BatchReadBlobs for %d digests", len(req.Digests))
		}
		digestsBefore := len(req.Digests)
		for _, res := range resp.Responses {
			st := status.FromProto(res.GetStatus())
			if st.Code() != codes.OK {
				return fmt.Errorf("failed to read chunk %s: %w", res.Digest, st.Err())
			}
			d := digest.FromProto(res.Digest)
			data, err := c.decodeForBatchRead(d, res.Data, res.Compressor)
			if err != nil {
				return err
			}
			err = c.opt.LocalCache.SetContent(ctx, d, dnames[d], data)
			if err != nil {
				return err
			}
			req.Digests = slices.DeleteFunc(req.Digests, func(reqDigest *rpb.Digest) bool {
				return proto.Equal(res.Digest, reqDigest)
			})
		}
		if len(req.Digests) == digestsBefore {
			return fmt.Errorf("BatchReadBlobs made no progress on %d digests", len(req.Digests))
		}
	}
	return nil
}
