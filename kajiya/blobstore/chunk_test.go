// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/testing/protocmp"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/server"
)

func TestSpliceAndSplitBlob_Success(t *testing.T) {
	ctx := t.Context()
	cfg := server.Config{MaxBatchTotalSizeBytes: 1024 * 1024}
	_, cas := setupTest(ctx, t, cfg)

	// Create three chunks of data.
	chunkData1 := []byte("hello ")
	chunkData2 := []byte("chunked ")
	chunkData3 := []byte("world")
	fullData := []byte("hello chunked world")

	chunkDigest1 := digest.SHA256.FromBytes(chunkData1).Proto()
	chunkDigest2 := digest.SHA256.FromBytes(chunkData2).Proto()
	chunkDigest3 := digest.SHA256.FromBytes(chunkData3).Proto()
	blobDigest := digest.SHA256.FromBytes(fullData).Proto()

	// 1. Upload the chunks via BatchUpdateBlobs.
	_, err := cas.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		Requests: []*repb.BatchUpdateBlobsRequest_Request{
			{Digest: chunkDigest1, Data: chunkData1},
			{Digest: chunkDigest2, Data: chunkData2},
			{Digest: chunkDigest3, Data: chunkData3},
		},
	})
	if err != nil {
		t.Fatalf("BatchUpdateBlobs failed: %v", err)
	}

	// 2. Call SpliceBlob to stitch them together.
	spliceResp, err := cas.SpliceBlob(ctx, &repb.SpliceBlobRequest{
		BlobDigest:       blobDigest,
		ChunkDigests:     []*repb.Digest{chunkDigest1, chunkDigest2, chunkDigest3},
		ChunkingFunction: repb.ChunkingFunction_FAST_CDC_2020,
	})
	if err != nil {
		t.Fatalf("SpliceBlob failed: %v", err)
	}
	if diff := cmp.Diff(blobDigest, spliceResp.GetBlobDigest(), protocmp.Transform()); diff != "" {
		t.Errorf("SpliceBlob response digest mismatch (-want +got):\n%s", diff)
	}

	// 3. Verify the reconstituted blob can be read directly via BatchReadBlobs.
	// Note: in real use case, it should be bytestream read.
	readResp, err := cas.BatchReadBlobs(ctx, &repb.BatchReadBlobsRequest{
		Digests: []*repb.Digest{blobDigest},
	})
	if err != nil {
		t.Fatalf("BatchReadBlobs failed: %v", err)
	}
	if len(readResp.Responses) != 1 {
		t.Fatalf("Expected 1 response, got %d", len(readResp.Responses))
	}
	if readResp.Responses[0].Status.Code != int32(codes.OK) {
		t.Fatalf("Expected OK status, got %v", readResp.Responses[0].Status)
	}
	if string(readResp.Responses[0].Data) != string(fullData) {
		t.Errorf("Expected blob data %q, got %q", string(fullData), string(readResp.Responses[0].Data))
	}

	// 4. Call SplitBlob to retrieve the chunk mapping.
	splitResp, err := cas.SplitBlob(ctx, &repb.SplitBlobRequest{
		BlobDigest: blobDigest,
	})
	if err != nil {
		t.Fatalf("SplitBlob failed: %v", err)
	}
	wantSplit := &repb.SplitBlobResponse{
		ChunkDigests:     []*repb.Digest{chunkDigest1, chunkDigest2, chunkDigest3},
		ChunkingFunction: repb.ChunkingFunction_FAST_CDC_2020,
	}
	if diff := cmp.Diff(wantSplit, splitResp, protocmp.Transform()); diff != "" {
		t.Errorf("SplitBlob response mismatch (-want +got):\n%s", diff)
	}
}

func TestSpliceBlob_ExistingBlob(t *testing.T) {
	ctx := t.Context()
	cfg := server.Config{MaxBatchTotalSizeBytes: 1024 * 1024}
	_, cas := setupTest(ctx, t, cfg)

	chunkData1 := []byte("foo ")
	chunkData2 := []byte("bar")
	fullData := []byte("foo bar")

	chunkDigest1 := digest.SHA256.FromBytes(chunkData1).Proto()
	chunkDigest2 := digest.SHA256.FromBytes(chunkData2).Proto()
	blobDigest := digest.SHA256.FromBytes(fullData).Proto()

	// Upload both the full blob and the chunks beforehand.
	_, err := cas.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		Requests: []*repb.BatchUpdateBlobsRequest_Request{
			{Digest: blobDigest, Data: fullData},
			{Digest: chunkDigest1, Data: chunkData1},
			{Digest: chunkDigest2, Data: chunkData2},
		},
	})
	if err != nil {
		t.Fatalf("BatchUpdateBlobs failed: %v", err)
	}

	// SpliceBlob should verify in-memory and succeed, registering the split mapping.
	_, err = cas.SpliceBlob(ctx, &repb.SpliceBlobRequest{
		BlobDigest:       blobDigest,
		ChunkDigests:     []*repb.Digest{chunkDigest1, chunkDigest2},
		ChunkingFunction: repb.ChunkingFunction_REP_MAX_CDC,
	})
	if err != nil {
		t.Fatalf("SpliceBlob on existing blob failed: %v", err)
	}

	splitResp, err := cas.SplitBlob(ctx, &repb.SplitBlobRequest{
		BlobDigest: blobDigest,
	})
	if err != nil {
		t.Fatalf("SplitBlob failed: %v", err)
	}
	wantSplit := &repb.SplitBlobResponse{
		ChunkDigests:     []*repb.Digest{chunkDigest1, chunkDigest2},
		ChunkingFunction: repb.ChunkingFunction_REP_MAX_CDC,
	}
	if diff := cmp.Diff(wantSplit, splitResp, protocmp.Transform()); diff != "" {
		t.Errorf("SplitBlob response mismatch (-want +got):\n%s", diff)
	}
}

func TestSpliceBlob_MissingChunk(t *testing.T) {
	ctx := t.Context()
	cfg := server.Config{MaxBatchTotalSizeBytes: 1024 * 1024}
	_, cas := setupTest(ctx, t, cfg)

	chunkData1 := []byte("hello")
	chunkData2 := []byte("missing")
	chunkDigest1 := digest.SHA256.FromBytes(chunkData1).Proto()
	chunkDigest2 := digest.SHA256.FromBytes(chunkData2).Proto()
	blobDigest := digest.SHA256.FromBytes([]byte("hellomissing")).Proto()

	// Only upload chunk 1.
	_, err := cas.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		Requests: []*repb.BatchUpdateBlobsRequest_Request{
			{Digest: chunkDigest1, Data: chunkData1},
		},
	})
	if err != nil {
		t.Fatalf("BatchUpdateBlobs failed: %v", err)
	}

	_, err = cas.SpliceBlob(ctx, &repb.SpliceBlobRequest{
		BlobDigest:   blobDigest,
		ChunkDigests: []*repb.Digest{chunkDigest1, chunkDigest2},
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("SpliceBlob=%v; want NotFound", err)
	}
}

func TestSpliceBlob_SizeMismatch(t *testing.T) {
	ctx := t.Context()
	cfg := server.Config{MaxBatchTotalSizeBytes: 1024 * 1024}
	_, cas := setupTest(ctx, t, cfg)

	chunkData1 := []byte("a")
	chunkDigest1 := digest.SHA256.FromBytes(chunkData1).Proto()
	// Claim the blob size is 10, but chunk size sum is 1.
	blobDigest := &repb.Digest{Hash: digest.SHA256.FromBytes([]byte("a")).Hash, SizeBytes: 10}

	_, err := cas.SpliceBlob(ctx, &repb.SpliceBlobRequest{
		BlobDigest:   blobDigest,
		ChunkDigests: []*repb.Digest{chunkDigest1},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("SpliceBlob=%v; want InvalidArgument", err)
	}
}

func TestSpliceBlob_HashMismatch(t *testing.T) {
	ctx := t.Context()
	cfg := server.Config{MaxBatchTotalSizeBytes: 1024 * 1024}
	_, cas := setupTest(ctx, t, cfg)

	chunkData1 := []byte("wrong content")
	chunkDigest1 := digest.SHA256.FromBytes(chunkData1).Proto()

	// Expect hash of "right content", which has same size (13 bytes).
	expectedDigest := digest.SHA256.FromBytes([]byte("right content")).Proto()

	_, err := cas.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		Requests: []*repb.BatchUpdateBlobsRequest_Request{
			{Digest: chunkDigest1, Data: chunkData1},
		},
	})
	if err != nil {
		t.Fatalf("BatchUpdateBlobs failed: %v", err)
	}

	_, err = cas.SpliceBlob(ctx, &repb.SpliceBlobRequest{
		BlobDigest:   expectedDigest,
		ChunkDigests: []*repb.Digest{chunkDigest1},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("SpliceBlob=%v; want InvalidArgument", err)
	}
}

func TestSplitBlob_NotFound(t *testing.T) {
	ctx := t.Context()
	cfg := server.Config{MaxBatchTotalSizeBytes: 1024 * 1024}
	_, cas := setupTest(ctx, t, cfg)

	blobData := []byte("no split info for this blob")
	blobDigest := digest.SHA256.FromBytes(blobData).Proto()

	// Upload blob via normal BatchUpdateBlobs without SpliceBlob.
	_, err := cas.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		Requests: []*repb.BatchUpdateBlobsRequest_Request{
			{Digest: blobDigest, Data: blobData},
		},
	})
	if err != nil {
		t.Fatalf("BatchUpdateBlobs failed: %v", err)
	}

	_, err = cas.SplitBlob(ctx, &repb.SplitBlobRequest{
		BlobDigest: blobDigest,
	})
	if status.Code(err) != codes.NotFound {
		t.Errorf("SplitBlob=%v; want NotFound", err)
	}
}
