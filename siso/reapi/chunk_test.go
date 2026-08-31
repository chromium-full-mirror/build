// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package reapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	bpb "google.golang.org/genproto/googleapis/bytestream"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/auth/cred"
)

type fakeChunkCAS struct {
	rpb.UnimplementedContentAddressableStorageServer
	bpb.UnimplementedByteStreamServer

	mu              sync.Mutex
	splitBlobMap    map[digest.Digest][]*rpb.Digest
	blobs           map[digest.Digest][]byte
	batchReadReqs   []*rpb.BatchReadBlobsRequest
	splitReqs       []*rpb.SplitBlobRequest
	byteStreamReads []string
}

func (f *fakeChunkCAS) SplitBlob(ctx context.Context, req *rpb.SplitBlobRequest) (*rpb.SplitBlobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.splitReqs = append(f.splitReqs, req)
	d := digest.FromProto(req.GetBlobDigest())
	chunks, ok := f.splitBlobMap[d]
	if !ok {
		return nil, status.Error(codes.NotFound, "no split info for blob")
	}
	return &rpb.SplitBlobResponse{
		ChunkDigests: chunks,
	}, nil
}

func (f *fakeChunkCAS) BatchReadBlobs(ctx context.Context, req *rpb.BatchReadBlobsRequest) (*rpb.BatchReadBlobsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batchReadReqs = append(f.batchReadReqs, req)
	resp := &rpb.BatchReadBlobsResponse{}
	for _, dp := range req.GetDigests() {
		d := digest.FromProto(dp)
		data, ok := f.blobs[d]
		if !ok {
			resp.Responses = append(resp.Responses, &rpb.BatchReadBlobsResponse_Response{
				Digest: dp,
				Status: &statuspb.Status{
					Code:    int32(codes.NotFound),
					Message: fmt.Sprintf("blob %s not found", d),
				},
			})
			continue
		}
		resp.Responses = append(resp.Responses, &rpb.BatchReadBlobsResponse_Response{
			Digest:     dp,
			Data:       data,
			Compressor: rpb.Compressor_IDENTITY,
			Status: &statuspb.Status{
				Code: int32(codes.OK),
			},
		})
	}
	return resp, nil
}

func (f *fakeChunkCAS) FindMissingBlobs(ctx context.Context, req *rpb.FindMissingBlobsRequest) (*rpb.FindMissingBlobsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := &rpb.FindMissingBlobsResponse{}
	for _, dp := range req.GetBlobDigests() {
		d := digest.FromProto(dp)
		if _, ok := f.blobs[d]; !ok {
			resp.MissingBlobDigests = append(resp.MissingBlobDigests, dp)
		}
	}
	return resp, nil
}

func (f *fakeChunkCAS) Read(req *bpb.ReadRequest, stream bpb.ByteStream_ReadServer) error {
	f.mu.Lock()
	f.byteStreamReads = append(f.byteStreamReads, req.GetResourceName())
	// Parse resource name: e.g. "{instance}/blobs/{hash}/{size}"
	parts := strings.Split(req.GetResourceName(), "/")
	var foundData []byte
	var found bool
	for d, data := range f.blobs {
		if strings.Contains(req.GetResourceName(), d.Hash) {
			foundData = data
			found = true
			break
		}
	}
	f.mu.Unlock()
	if !found {
		return status.Errorf(codes.NotFound, "resource %s not found in %v", req.GetResourceName(), parts)
	}
	return stream.Send(&bpb.ReadResponse{
		Data: foundData,
	})
}

type fakeCapabilities struct {
	rpb.UnimplementedCapabilitiesServer

	capabilities *rpb.ServerCapabilities
}

func (f *fakeCapabilities) GetCapabilities(ctx context.Context, req *rpb.GetCapabilitiesRequest) (*rpb.ServerCapabilities, error) {
	if f.capabilities != nil {
		return f.capabilities, nil
	}
	return &rpb.ServerCapabilities{
		CacheCapabilities: &rpb.CacheCapabilities{
			DigestFunctions: []rpb.DigestFunction_Value{
				rpb.DigestFunction_SHA256,
			},
			ActionCacheUpdateCapabilities: &rpb.ActionCacheUpdateCapabilities{
				UpdateEnabled: true,
			},
			SplitBlobSupport: true,
		},
	}, nil
}

func setupChunkTestClient(t *testing.T, fakeCAS *fakeChunkCAS, opt Option) (*Client, *LocalCache) {
	t.Helper()
	ctx := t.Context()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	rpb.RegisterContentAddressableStorageServer(srv, fakeCAS)
	bpb.RegisterByteStreamServer(srv, fakeCAS)
	rpb.RegisterCapabilitiesServer(srv, &fakeCapabilities{})
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	cacheDir := t.TempDir()
	localCache, err := NewLocalCache(cacheDir)
	if err != nil {
		t.Fatalf("NewLocalCache: %v", err)
	}
	opt.LocalCache = localCache
	if opt.Instance == "" {
		opt.Instance = "projects/test/instances/default"
	}
	client, err := NewFromConn(ctx, opt, cred.Cred{}, conn, conn, conn)
	if err != nil {
		t.Fatalf("NewFromConn: %v", err)
	}
	err = client.Init(ctx)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return client, localCache
}

func makeChunkDigests(sizes ...int64) []*rpb.Digest {
	var res []*rpb.Digest
	for i, sz := range sizes {
		d := digest.Digest{
			Hash:      fmt.Sprintf("hash-%d", i),
			SizeBytes: sz,
		}
		res = append(res, d.Proto())
	}
	return res
}

func TestChunkReadBatch_Empty(t *testing.T) {
	cacheDir := t.TempDir()
	localCache, err := NewLocalCache(cacheDir)
	if err != nil {
		t.Fatalf("NewLocalCache: %v", err)
	}
	c := &Client{
		opt: Option{
			LocalCache:          localCache,
			chunkBatchThreshold: 1000,
		},
		digestFn: digest.SHA256,
	}
	batches := slices.Collect(c.chunkReadBatch(t.Context(), nil))
	if len(batches) != 0 {
		t.Errorf("got %d batches, want 0", len(batches))
	}
}

func TestChunkReadBatch_SingleBatchBelowLimit(t *testing.T) {
	cacheDir := t.TempDir()
	localCache, err := NewLocalCache(cacheDir)
	if err != nil {
		t.Fatalf("NewLocalCache: %v", err)
	}
	c := &Client{
		opt: Option{
			LocalCache:          localCache,
			chunkBatchThreshold: 1000,
		},
		digestFn: digest.SHA256,
	}
	digests := makeChunkDigests(100, 200, 300)
	batches := slices.Collect(c.chunkReadBatch(t.Context(), digests))
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}
	if len(batches[0]) != 3 {
		t.Errorf("got %d chunks in batch 0, want 3", len(batches[0]))
	}
}

func TestChunkReadBatch_MultipleBatchesAboveLimit(t *testing.T) {
	cacheDir := t.TempDir()
	localCache, err := NewLocalCache(cacheDir)
	if err != nil {
		t.Fatalf("NewLocalCache: %v", err)
	}
	c := &Client{
		opt: Option{
			LocalCache:          localCache,
			chunkBatchThreshold: 1000,
		},
		digestFn: digest.SHA256,
	}
	digests := makeChunkDigests(400, 400, 400, 400) // 4 chunks of 400 with limit 1000 -> 2 batches
	batches := slices.Collect(c.chunkReadBatch(t.Context(), digests))
	if len(batches) != 2 {
		t.Fatalf("got %d batches, want 2", len(batches))
	}
	if len(batches[0]) != 2 || len(batches[1]) != 2 {
		t.Errorf("got batch sizes [%d, %d], want [2, 2]", len(batches[0]), len(batches[1]))
	}
}

func TestChunkReadBatch_SingleLargeChunkAboveLimit(t *testing.T) {
	cacheDir := t.TempDir()
	localCache, err := NewLocalCache(cacheDir)
	if err != nil {
		t.Fatalf("NewLocalCache: %v", err)
	}
	c := &Client{
		opt: Option{
			LocalCache:          localCache,
			chunkBatchThreshold: 1000,
		},
		digestFn: digest.SHA256,
	}
	digests := makeChunkDigests(2000)
	batches := slices.Collect(c.chunkReadBatch(t.Context(), digests))
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}
	if len(batches[0]) != 1 {
		t.Errorf("got %d chunks, want 1", len(batches[0]))
	}
}

func TestChunkReadBatch_MixedChunkSizes(t *testing.T) {
	cacheDir := t.TempDir()
	localCache, err := NewLocalCache(cacheDir)
	if err != nil {
		t.Fatalf("NewLocalCache: %v", err)
	}
	c := &Client{
		opt: Option{
			LocalCache:          localCache,
			chunkBatchThreshold: 1000,
		},
		digestFn: digest.SHA256,
	}
	digests := makeChunkDigests(300, 400, 500, 2000, 300, 400)
	batches := slices.Collect(c.chunkReadBatch(t.Context(), digests))
	// [300, 400] (700 < 1000, adding 500 exceeds 1000 -> yield [300, 400])
	// [500] (500 < 1000, adding 2000 exceeds 1000 -> yield [500])
	// [2000] (single chunk >= 1000 -> yield [2000])
	// [300, 400] (remaining -> yield [300, 400])
	if len(batches) != 4 {
		t.Fatalf("got %d batches, want 4", len(batches))
	}
	if len(batches[0]) != 2 || len(batches[1]) != 1 || len(batches[2]) != 1 || len(batches[3]) != 2 {
		t.Errorf("got batch sizes [%d, %d, %d, %d], want [2, 1, 1, 2]",
			len(batches[0]), len(batches[1]), len(batches[2]), len(batches[3]))
	}
}

func TestChunkReader_DisabledNoLocalCache(t *testing.T) {
	ctx := t.Context()
	fakeCAS := &fakeChunkCAS{
		splitBlobMap: make(map[digest.Digest][]*rpb.Digest),
		blobs:        make(map[digest.Digest][]byte),
	}
	client, _ := setupChunkTestClient(t, fakeCAS, Option{})
	client.opt.LocalCache = nil

	d := digest.Digest{Hash: "test-hash", SizeBytes: 100}
	_, err := client.chunkReader(ctx, d, "file.txt")
	if err == nil {
		t.Fatal("chunkReader succeeded without local cache; want error")
	}
	if !strings.Contains(err.Error(), "no local cache") {
		t.Errorf("got err=%v, want 'no local cache'", err)
	}
}

func TestChunkReader_SplitBlobNotFound(t *testing.T) {
	ctx := t.Context()
	fakeCAS := &fakeChunkCAS{
		splitBlobMap: make(map[digest.Digest][]*rpb.Digest),
		blobs:        make(map[digest.Digest][]byte),
	}
	client, _ := setupChunkTestClient(t, fakeCAS, Option{})

	d := digest.Digest{Hash: "test-hash", SizeBytes: 100}
	_, err := client.chunkReader(ctx, d, "file.txt")
	if status.Code(err) != codes.NotFound {
		t.Fatalf("chunkReader got err=%v, want NotFound", err)
	}
}

func TestChunkReader_BytestreamioFallbackOnNotFound(t *testing.T) {
	ctx := t.Context()
	raw := []byte("hello bytestream fallback world")
	d := digest.SHA256.FromBytes(raw)

	fakeCAS := &fakeChunkCAS{
		splitBlobMap: make(map[digest.Digest][]*rpb.Digest), // no split info -> returns NotFound
		blobs: map[digest.Digest][]byte{
			d: raw,
		},
	}
	client, _ := setupChunkTestClient(t, fakeCAS, Option{
		ChunkedBlobsThreshold: 0,
	})

	rc, err := client.bytestreamioOpen(ctx, d, "fallback.txt")
	if err != nil {
		t.Fatalf("bytestreamioOpen: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("got %q, want %q", string(got), string(raw))
	}
	if len(fakeCAS.splitReqs) != 1 {
		t.Errorf("got %d splitReqs, want 1", len(fakeCAS.splitReqs))
	}
	if len(fakeCAS.byteStreamReads) != 1 {
		t.Errorf("got %d byteStreamReads, want 1", len(fakeCAS.byteStreamReads))
	}
}

func TestChunkReader_SuccessMultiChunk(t *testing.T) {
	ctx := t.Context()
	chunkData0 := bytes.Repeat([]byte("A"), 40*1024)
	chunkData1 := bytes.Repeat([]byte("B"), 40*1024)
	chunkData2 := bytes.Repeat([]byte("C"), 20*1024)
	fullData := append(append(slices.Clone(chunkData0), chunkData1...), chunkData2...)

	d0 := digest.SHA256.FromBytes(chunkData0)
	d1 := digest.SHA256.FromBytes(chunkData1)
	d2 := digest.SHA256.FromBytes(chunkData2)
	fullDigest := digest.SHA256.FromBytes(fullData)

	fakeCAS := &fakeChunkCAS{
		splitBlobMap: map[digest.Digest][]*rpb.Digest{
			fullDigest: {d0.Proto(), d1.Proto(), d2.Proto()},
		},
		blobs: map[digest.Digest][]byte{
			d0: chunkData0,
			d1: chunkData1,
			d2: chunkData2,
		},
	}
	client, localCache := setupChunkTestClient(t, fakeCAS, Option{
		CompressedBlob: 1024 * 1024,
	})

	r, err := client.chunkReader(ctx, fullDigest, "multi.dat")
	if err != nil {
		t.Fatalf("chunkReader: %v", err)
	}
	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, fullData) {
		t.Fatalf("content mismatch: got %d bytes, want %d bytes", len(got), len(fullData))
	}

	// Verify all chunks are cached locally
	for i, d := range []digest.Digest{d0, d1, d2} {
		if !localCache.HasContent(ctx, d) {
			t.Errorf("chunk %d (%s) not found in local cache", i, d)
		}
	}
}

func TestChunkReader_PreCachedChunks(t *testing.T) {
	ctx := t.Context()
	chunkData0 := []byte("ChunkZeroData")
	chunkData1 := []byte("ChunkOneData")
	chunkData2 := []byte("ChunkTwoData")
	fullData := append(append(slices.Clone(chunkData0), chunkData1...), chunkData2...)

	d0 := digest.SHA256.FromBytes(chunkData0)
	d1 := digest.SHA256.FromBytes(chunkData1)
	d2 := digest.SHA256.FromBytes(chunkData2)
	fullDigest := digest.SHA256.FromBytes(fullData)

	fakeCAS := &fakeChunkCAS{
		splitBlobMap: map[digest.Digest][]*rpb.Digest{
			fullDigest: {d0.Proto(), d1.Proto(), d2.Proto()},
		},
		blobs: map[digest.Digest][]byte{
			d0: chunkData0,
			d1: chunkData1,
			d2: chunkData2,
		},
	}
	client, localCache := setupChunkTestClient(t, fakeCAS, Option{
		ByteStreamReadThreshold: 1 * 1024 * 1024,
		CompressedBlob:          1024 * 1024,
	})

	// Pre-populate Chunk 0 and Chunk 2 in localCache
	if err := localCache.SetContent(ctx, d0, "precached#0", chunkData0); err != nil {
		t.Fatalf("SetContent d0: %v", err)
	}
	if err := localCache.SetContent(ctx, d2, "precached#2", chunkData2); err != nil {
		t.Fatalf("SetContent d2: %v", err)
	}

	r, err := client.chunkReader(ctx, fullDigest, "precached.dat")
	if err != nil {
		t.Fatalf("chunkReader: %v", err)
	}
	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, fullData) {
		t.Fatalf("content mismatch: got %q, want %q", string(got), string(fullData))
	}

	// Verify only Chunk 1 was requested from CAS
	var requestedDigests []digest.Digest
	for _, req := range fakeCAS.batchReadReqs {
		for _, dp := range req.GetDigests() {
			requestedDigests = append(requestedDigests, digest.FromProto(dp))
		}
	}
	if len(requestedDigests) != 1 || requestedDigests[0] != d1 {
		t.Errorf("requested digests = %v; want only [%s]", requestedDigests, d1)
	}
}

func TestChunkReader_SingleLargeChunk(t *testing.T) {
	ctx := t.Context()
	chunkData := bytes.Repeat([]byte("L"), 200*1024)
	d := digest.SHA256.FromBytes(chunkData)

	fakeCAS := &fakeChunkCAS{
		splitBlobMap: map[digest.Digest][]*rpb.Digest{
			d: {d.Proto()},
		},
		blobs: map[digest.Digest][]byte{
			d: chunkData,
		},
	}
	client, localCache := setupChunkTestClient(t, fakeCAS, Option{
		CompressedBlob: 50 * 1024, // batch limit 50KB, chunk is 200KB so chunkReadBatch yields len(chunks)==1
	})

	r, err := client.chunkReader(ctx, d, "large_chunk.dat")
	if err != nil {
		t.Fatalf("chunkReader: %v", err)
	}
	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, chunkData) {
		t.Fatalf("content mismatch: got %d bytes, want %d bytes", len(got), len(chunkData))
	}
	if !localCache.HasContent(ctx, d) {
		t.Errorf("chunk %s not found in local cache", d)
	}
}

func TestChunkReader_VaryingBufferSizes(t *testing.T) {
	ctx := t.Context()
	chunk0 := []byte("0123456789")
	chunk1 := []byte("abcdefghij")
	chunk2 := []byte("klmnopqrst")
	fullData := append(append(slices.Clone(chunk0), chunk1...), chunk2...)

	d0 := digest.SHA256.FromBytes(chunk0)
	d1 := digest.SHA256.FromBytes(chunk1)
	d2 := digest.SHA256.FromBytes(chunk2)
	fullDigest := digest.SHA256.FromBytes(fullData)

	for _, bufSize := range []int{1, 3, 7, 10, 15, 30, 100} {
		t.Run(fmt.Sprintf("bufSize_%d", bufSize), func(t *testing.T) {
			fakeCAS := &fakeChunkCAS{
				splitBlobMap: map[digest.Digest][]*rpb.Digest{
					fullDigest: {d0.Proto(), d1.Proto(), d2.Proto()},
				},
				blobs: map[digest.Digest][]byte{
					d0: chunk0,
					d1: chunk1,
					d2: chunk2,
				},
			}
			client, _ := setupChunkTestClient(t, fakeCAS, Option{
				CompressedBlob: 1000,
			})

			r, err := client.chunkReader(ctx, fullDigest, "vary.dat")
			if err != nil {
				t.Fatalf("chunkReader: %v", err)
			}
			defer r.Close()

			var buf bytes.Buffer
			tmp := make([]byte, bufSize)
			for {
				n, err := r.Read(tmp)
				if n > 0 {
					buf.Write(tmp[:n])
				}
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("Read error: %v", err)
				}
			}
			if !bytes.Equal(buf.Bytes(), fullData) {
				t.Errorf("got %q, want %q", buf.String(), string(fullData))
			}
		})
	}
}

func TestChunkReader_DigestMismatch(t *testing.T) {
	ctx := t.Context()
	chunkData0 := []byte("ValidChunk0")
	chunkData1 := []byte("ValidChunk1")
	corruptedData1 := []byte("CorruptChk1")
	fullData := append(slices.Clone(chunkData0), chunkData1...)

	d0 := digest.SHA256.FromBytes(chunkData0)
	corruptD1 := digest.SHA256.FromBytes(corruptedData1)
	fullDigest := digest.SHA256.FromBytes(fullData)

	fakeCAS := &fakeChunkCAS{
		splitBlobMap: map[digest.Digest][]*rpb.Digest{
			fullDigest: {d0.Proto(), corruptD1.Proto()},
		},
		blobs: map[digest.Digest][]byte{
			d0:        chunkData0,
			corruptD1: corruptedData1,
		},
	}
	client, _ := setupChunkTestClient(t, fakeCAS, Option{
		CompressedBlob: 1000,
	})

	r, err := client.chunkReader(ctx, fullDigest, "corrupt.dat")
	if err != nil {
		t.Fatalf("chunkReader: %v", err)
	}
	defer r.Close()

	_, err = io.ReadAll(r)
	if err == nil {
		t.Fatal("expected digest mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "chunked blob digest mismatch") {
		t.Errorf("got err=%v, want 'chunked blob digest mismatch'", err)
	}
}

func TestChunkReader_SizeMismatch(t *testing.T) {
	ctx := t.Context()
	chunkData0 := []byte("Short")
	d0 := digest.SHA256.FromBytes(chunkData0)
	// Declare expected size as 100 bytes even though chunk is only 5 bytes
	fullDigest := digest.Digest{Hash: "somehash", SizeBytes: 100}

	fakeCAS := &fakeChunkCAS{
		splitBlobMap: map[digest.Digest][]*rpb.Digest{
			fullDigest: {d0.Proto()},
		},
		blobs: map[digest.Digest][]byte{
			d0: chunkData0,
		},
	}
	client, _ := setupChunkTestClient(t, fakeCAS, Option{
		CompressedBlob: 1000,
	})

	r, err := client.chunkReader(ctx, fullDigest, "mismatch.dat")
	if err != nil {
		t.Fatalf("chunkReader: %v", err)
	}
	defer r.Close()

	_, err = io.ReadAll(r)
	if err == nil {
		t.Fatal("expected size mismatch / digest mismatch error, got nil")
	}
}

func TestChunkReader_Close(t *testing.T) {
	ctx := t.Context()
	chunkData0 := bytes.Repeat([]byte("X"), 1024)
	chunkData1 := bytes.Repeat([]byte("Y"), 1024)
	fullData := append(slices.Clone(chunkData0), chunkData1...)

	d0 := digest.SHA256.FromBytes(chunkData0)
	d1 := digest.SHA256.FromBytes(chunkData1)
	fullDigest := digest.SHA256.FromBytes(fullData)

	fakeCAS := &fakeChunkCAS{
		splitBlobMap: map[digest.Digest][]*rpb.Digest{
			fullDigest: {d0.Proto(), d1.Proto()},
		},
		blobs: map[digest.Digest][]byte{
			d0: chunkData0,
			d1: chunkData1,
		},
	}
	client, _ := setupChunkTestClient(t, fakeCAS, Option{
		CompressedBlob: 10000,
	})

	r, err := client.chunkReader(ctx, fullDigest, "close.dat")
	if err != nil {
		t.Fatalf("chunkReader: %v", err)
	}

	buf := make([]byte, 100)
	n, err := r.Read(buf)
	if err != nil || n != 100 {
		t.Fatalf("Read: n=%d err=%v", n, err)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if r.chunks != nil {
		t.Errorf("r.chunks not nilled after Close: %v", r.chunks)
	}
	if r.rd != nil {
		t.Errorf("r.rd not nilled after Close: %v", r.rd)
	}
}
