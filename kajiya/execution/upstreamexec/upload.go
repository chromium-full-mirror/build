// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package upstreamexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"

	"github.com/google/uuid"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/execution/model"
)

// Limits for CAS requests sent upstream. The batch size limit is
// Executor.maxBatchSize, which also honors the upstream's advertised limit.
const (
	// findMissingChunk is the maximum number of digests sent in a single
	// FindMissingBlobsRequest, which keeps large input trees well under any
	// server's request size limit.
	findMissingChunk = 1000

	// maxBatchBlobs is the maximum number of blobs in a single
	// BatchUpdateBlobs or BatchReadBlobs request. REAPI's
	// max_batch_total_size_bytes counts blob sizes only, but each blob also
	// adds its digest and other proto framing, under 200 bytes even for
	// SHA-512. Bounding the count keeps that overhead within the headroom
	// that defaultMaxBatchSize leaves below gRPC's default 4 MiB message
	// limit, however small the blobs are.
	maxBatchBlobs = 1000

	// byteStreamThreshold is the size from which a blob is worth its own
	// ByteStream round trip instead of sharing a batch request with other
	// blobs. Blobs larger than Executor.maxBatchSize use ByteStream
	// regardless.
	byteStreamThreshold = 2 * 1024 * 1024

	// byteStreamChunkSize is the payload size of each ByteStream Write
	// request, well under the default 4 MiB gRPC receive limit of the
	// upstream server.
	byteStreamChunkSize = 1024 * 1024
)

// collectInputDigests returns every blob digest the upstream needs to run the
// given action: the Action proto, the Command proto, every Directory in the
// input tree, and every input file, each once. The empty digest is left out.
func collectInputDigests(action *model.Action) []digest.Digest {
	seen := make(map[digest.Digest]struct{}, 64)
	out := make([]digest.Digest, 0, 64)
	add := func(d digest.Digest) {
		if d == action.Fn.Empty() {
			return
		}
		if _, ok := seen[d]; ok {
			return
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}

	// Add the Action and Command protos.
	add(action.ActionDigest)
	add(action.CommandDigest)

	// Walk every KajiyaDirectory in the input trie. KajiyaDirectory.Digest is
	// the digest of the Directory proto blob; KajiyaFile.Digest is the digest
	// of the file content blob. Both must be present upstream.
	it := action.InputTrie.Root().Iterator()
	for {
		_, kd, ok := it.Next()
		if !ok {
			break
		}
		add(kd.Digest)
		for _, f := range kd.Files {
			add(f.Digest)
		}
	}
	return out
}

// findMissing asks the upstream which of digests are absent, in requests of
// at most findMissingChunk digests.
func (e *Executor) findMissing(ctx context.Context, fn digest.Function, digests []digest.Digest) ([]digest.Digest, error) {
	var missing []digest.Digest
	for chunk := range slices.Chunk(digests, findMissingChunk) {
		resp, err := e.casClient.FindMissingBlobs(ctx, &repb.FindMissingBlobsRequest{
			InstanceName:   e.instanceName,
			BlobDigests:    toProtoDigests(chunk),
			DigestFunction: fn.Value(),
		})
		if err != nil {
			return nil, fmt.Errorf("FindMissingBlobs: %w", err)
		}
		for _, pd := range resp.MissingBlobDigests {
			d, err := fn.FromProto(pd)
			if err != nil {
				return nil, fmt.Errorf("FindMissingBlobs returned invalid digest: %w", err)
			}
			missing = append(missing, d)
		}
	}
	return missing, nil
}

// upload sends every blob in digests to the upstream CAS, in the requests
// that forEachBatch picks.
func (e *Executor) upload(ctx context.Context, fn digest.Function, digests []digest.Digest) error {
	return e.forEachBatch(digests,
		func(batch []digest.Digest) error { return e.uploadBatch(ctx, fn, batch) },
		func(d digest.Digest) error { return e.uploadByteStream(ctx, fn, d) })
}

// forEachBatch splits digests into the requests that transfer them, and stops
// at the first error. Each blob of at least byteStreamThreshold, or over
// Executor.maxBatchSize, goes to single, in a ByteStream call of its own. The
// other blobs go to batch, packed into slices of at most maxBatchBlobs blobs
// and Executor.maxBatchSize bytes. batch must not keep its slice.
func (e *Executor) forEachBatch(digests []digest.Digest, batch func([]digest.Digest) error, single func(digest.Digest) error) error {
	var pending []digest.Digest
	var pendingSize int64
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		err := batch(pending)
		pending, pendingSize = pending[:0], 0
		return err
	}
	for _, d := range digests {
		// A blob larger than the upstream's batch limit must use ByteStream,
		// because a batch request would be rejected. This matters when the
		// upstream advertises a limit below byteStreamThreshold.
		if d.SizeBytes >= byteStreamThreshold || d.SizeBytes > e.maxBatchSize {
			if err := flush(); err != nil {
				return err
			}
			if err := single(d); err != nil {
				return err
			}
			continue
		}
		// Pack small blobs until adding this one would exceed either batch
		// limit.
		if (pendingSize+d.SizeBytes > e.maxBatchSize && len(pending) > 0) || len(pending) == maxBatchBlobs {
			if err := flush(); err != nil {
				return err
			}
		}
		pending = append(pending, d)
		pendingSize += d.SizeBytes
	}
	return flush()
}

// uploadBatch sends all of batch in a single BatchUpdateBlobs request,
// reading each blob from the local CAS. It fails on the first blob the
// upstream reports as not stored.
func (e *Executor) uploadBatch(ctx context.Context, fn digest.Function, batch []digest.Digest) error {
	// Read the blobs from the local CAS.
	reqs := make([]*repb.BatchUpdateBlobsRequest_Request, 0, len(batch))
	for _, d := range batch {
		data, err := e.cas.Get(fn, d)
		if err != nil {
			return fmt.Errorf("read local blob %s for upload: %w", d, err)
		}
		reqs = append(reqs, &repb.BatchUpdateBlobsRequest_Request{
			Digest: d.Proto(),
			Data:   data,
		})
	}

	// Send them and check each blob's status.
	resp, err := e.casClient.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		InstanceName:   e.instanceName,
		Requests:       reqs,
		DigestFunction: fn.Value(),
	})
	if err != nil {
		return fmt.Errorf("BatchUpdateBlobs: %w", err)
	}
	for _, r := range resp.Responses {
		if r.Status == nil || codes.Code(r.Status.Code) == codes.OK {
			continue
		}
		return fmt.Errorf("BatchUpdateBlobs blob %s/%d: %s",
			r.Digest.GetHash(), r.Digest.GetSizeBytes(), r.Status.Message)
	}
	return nil
}

// uploadByteStream streams a single blob from the local CAS to the upstream
// CAS via ByteStream.Write. It succeeds if the upstream commits the full blob,
// including when the upstream already has it and ends the write early.
func (e *Executor) uploadByteStream(ctx context.Context, fn digest.Function, d digest.Digest) error {
	// Open the local blob and the upload stream. The stream's context is
	// canceled on return, which releases the stream if an error leaves it
	// open.
	rc, err := e.cas.Open(fn, d, 0, 0)
	if err != nil {
		return fmt.Errorf("open local blob %s for ByteStream upload: %w", d, err)
	}
	defer rc.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := e.bsClient.Write(ctx)
	if err != nil {
		return fmt.Errorf("ByteStream.Write open: %w", err)
	}

	// Send the blob in chunks. A server that already has the blob may end the
	// stream early, even in the middle of an upload; Send then returns io.EOF
	// and CloseAndRecv reports the outcome. path.Join drops an empty
	// instance name and digest function segment.
	resourceName := path.Join(e.instanceName, "uploads", uuid.NewString(), "blobs", fn.ResourceNameSegment(), d.Hash, strconv.FormatInt(d.SizeBytes, 10))
	buf := make([]byte, min(d.SizeBytes, byteStreamChunkSize))
	for sent := int64(0); sent < d.SizeBytes; {
		n, err := io.ReadFull(rc, buf[:min(d.SizeBytes-sent, int64(len(buf)))])
		if err != nil {
			return fmt.Errorf("read local blob %s at offset %d: %w", d, sent, err)
		}
		req := &bspb.WriteRequest{
			WriteOffset: sent,
			FinishWrite: sent+int64(n) == d.SizeBytes,
			Data:        buf[:n],
		}
		if sent == 0 {
			req.ResourceName = resourceName
		}
		if err := stream.Send(req); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("ByteStream.Write send (offset=%d): %w", sent, err)
		}
		sent += int64(n)
	}

	// Check the server's verdict.
	resp, err := stream.CloseAndRecv()
	if err != nil {
		// Some servers reject a write of a blob they already have with
		// AlreadyExists instead of ending it early; the blob is there either
		// way.
		if status.Code(err) == codes.AlreadyExists {
			return nil
		}
		return fmt.Errorf("ByteStream.Write close: %w", err)
	}
	if resp.CommittedSize != d.SizeBytes {
		return fmt.Errorf("ByteStream.Write committed_size mismatch for %s: got %d, want %d",
			d, resp.CommittedSize, d.SizeBytes)
	}
	return nil
}

// toProtoDigests converts a slice of internal digests to repb.Digest pointers.
func toProtoDigests(ds []digest.Digest) []*repb.Digest {
	out := make([]*repb.Digest, len(ds))
	for i, d := range ds {
		out[i] = d.Proto()
	}
	return out
}
