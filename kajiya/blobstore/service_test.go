// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"go.chromium.org/build/kajiya/digest"
)

// bufferSize is the size of the in-memory buffer.
const bufferSize = 1024 * 1024

// startTestServer sets up a gRPC server listening on a bufconn listener.
// It returns the listener (to dial to) and a cleanup function.
func startTestServer(t testing.TB, cas *ContentAddressableStorage) *bufconn.Listener {
	t.Helper()

	// Create an in-memory listener
	lis := bufconn.Listen(bufferSize)

	// Create a standard gRPC server
	s := grpc.NewServer()

	// Register the service implementation
	Register(s, cas)

	// Start serving in a background goroutine
	go func() {
		if err := s.Serve(lis); err != nil {
			t.Errorf("Failed to serve gRPC server: %v", err)
		}
	}()

	t.Cleanup(func() {
		s.Stop()
		if err := lis.Close(); err != nil {
			t.Errorf("Failed to close bufnet listener: %v", err)
		}
	})

	return lis
}

func setupTest(ctx context.Context, t testing.TB) (bspb.ByteStreamClient, repb.ContentAddressableStorageClient) {
	t.Helper()

	// Setup CAS.
	dataDir := t.TempDir()
	cas, err := New(ctx, dataDir)
	if err != nil {
		t.Fatalf("Failed to create CAS: %v", err)
	}

	// Start the server
	lis := startTestServer(t, cas)

	// Create a client that dials the in-memory listener
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return bspb.NewByteStreamClient(conn), repb.NewContentAddressableStorageClient(conn)
}

func TestReadWrite(t *testing.T) {
	ctx := t.Context()
	client, _ := setupTest(ctx, t)

	// Generate random data larger than maxChunkSize (2MB) to force chunking
	blobSize := int64(5 * 1024 * 1024)
	blobData := make([]byte, blobSize)
	if _, err := rand.Read(blobData); err != nil {
		t.Fatalf("Failed to generate random data: %v", err)
	}
	d := digest.FromBlob(blobData)

	// --- Test Write ---
	uploadID := uuid.New()
	writeResourceName := fmt.Sprintf("test-instance/uploads/%s/blobs/%s/%d", uploadID, d.Hash, d.Size)

	stream, err := client.Write(ctx)
	if err != nil {
		t.Fatalf("Failed to create Write stream: %v", err)
	}

	// Send data in 1MB chunks
	chunkSize := int64(1024 * 1024)
	offset := int64(0)
	for offset < blobSize {
		end := min(offset+chunkSize, blobSize)
		req := &bspb.WriteRequest{
			ResourceName: writeResourceName,
			WriteOffset:  offset,
			FinishWrite:  end == blobSize,
			Data:         blobData[offset:end],
		}

		if err := stream.Send(req); err != nil {
			t.Fatalf("Failed to send chunk at offset %d: %v", offset, err)
		}
		offset = end
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("Failed to CloseAndRecv: %v", err)
	}
	if got, want := resp.CommittedSize, blobSize; got != want {
		t.Errorf("CommittedSize = %d, want %d", got, want)
	}

	// --- Test Read ---
	readResourceName := fmt.Sprintf("test-instance/blobs/%s/%d", d.Hash, d.Size)
	readStream, err := client.Read(ctx, &bspb.ReadRequest{
		ResourceName: readResourceName,
	})
	if err != nil {
		t.Fatalf("Failed to create Read stream: %v", err)
	}

	var readBuf bytes.Buffer
	for {
		chunk, err := readStream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Failed to Recv read chunk: %v", err)
		}
		readBuf.Write(chunk.Data)
	}

	if !bytes.Equal(readBuf.Bytes(), blobData) {
		t.Errorf("Read data mismatch")
	}
}

func TestBatchOperations(t *testing.T) {
	ctx := t.Context()
	_, casClient := setupTest(ctx, t)

	// Create some blobs
	blobs := [][]byte{
		[]byte("blob1"),
		[]byte("blob2"),
		[]byte("blob3"),
	}
	digests := make([]*repb.Digest, len(blobs))
	for i, b := range blobs {
		digests[i] = digest.FromBlob(b).ToProto()
	}

	// 1. Test FindMissingBlobs - all should be missing
	missingResp, err := casClient.FindMissingBlobs(ctx, &repb.FindMissingBlobsRequest{
		BlobDigests: digests,
	})
	if err != nil {
		t.Fatalf("FindMissingBlobs failed: %v", err)
	}
	if got, want := len(missingResp.MissingBlobDigests), 3; got != want {
		t.Errorf("Got %d missing blobs, want %d", got, want)
	}

	// 2. Test BatchUpdateBlobs - upload first two
	updateReqs := []*repb.BatchUpdateBlobsRequest_Request{
		{Digest: digests[0], Data: blobs[0]},
		{Digest: digests[1], Data: blobs[1]},
	}
	updateResp, err := casClient.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		Requests: updateReqs,
	})
	if err != nil {
		t.Fatalf("BatchUpdateBlobs failed: %v", err)
	}
	for _, r := range updateResp.Responses {
		if r.Status.Code != 0 {
			t.Errorf("BatchUpdateBlobs response error for %s: %v", r.Digest.Hash, r.Status)
		}
	}

	// 3. Test FindMissingBlobs again - only the third should be missing
	missingResp, err = casClient.FindMissingBlobs(ctx, &repb.FindMissingBlobsRequest{
		BlobDigests: digests,
	})
	if err != nil {
		t.Fatalf("FindMissingBlobs failed: %v", err)
	}
	if got, want := len(missingResp.MissingBlobDigests), 1; got != want {
		t.Errorf("Got %d missing blobs, want %d", got, want)
	}
	if got, want := missingResp.MissingBlobDigests[0].Hash, digests[2].Hash; got != want {
		t.Errorf("Hash of missing blob is %v, want %v", got, want)
	}

	// 4. Test BatchReadBlobs
	readResp, err := casClient.BatchReadBlobs(ctx, &repb.BatchReadBlobsRequest{
		Digests: digests,
	})
	if err != nil {
		t.Fatalf("BatchReadBlobs failed: %v", err)
	}
	if got, want := len(readResp.Responses), 3; got != want {
		t.Fatalf("Got %d responses, want %d", got, want)
	}
	for i, r := range readResp.Responses {
		if i < 2 {
			if got, want := codes.Code(r.Status.Code), codes.OK; got != want {
				t.Errorf("Response code for blob %d got %v, want %v", i, got, want)
			}
			if got, want := r.Data, blobs[i]; !bytes.Equal(got, want) {
				t.Errorf("Data for blob %d got %v, want %v", i, got, want)
			}
		} else {
			if got, want := codes.Code(r.Status.Code), codes.NotFound; got != want {
				t.Errorf("Response code for blob %d got %v, want %v", i, got, want)
			}
		}
	}
}

func TestReadWriteZstd(t *testing.T) {
	ctx := t.Context()
	client, _ := setupTest(ctx, t)

	// Generate random data
	blobSize := int64(1 * 1024 * 1024)
	blobData := make([]byte, blobSize)
	if _, err := rand.Read(blobData); err != nil {
		t.Fatalf("Failed to generate random data: %v", err)
	}
	d := digest.FromBlob(blobData)

	// Compress the data manually
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("Failed to create zstd writer: %v", err)
	}
	compressedData := encoder.EncodeAll(blobData, nil)

	// --- Test Write (Compressed) ---
	uploadID := uuid.New()
	// Resource name format: {instance_name}/uploads/{uuid}/compressed-blobs/zstd/{uncompressed_hash}/{uncompressed_size}
	writeResourceName := fmt.Sprintf("test-instance/uploads/%s/compressed-blobs/zstd/%s/%d", uploadID, d.Hash, d.Size)

	stream, err := client.Write(ctx)
	if err != nil {
		t.Fatalf("Failed to create Write stream: %v", err)
	}

	// Send compressed data
	if err := stream.Send(&bspb.WriteRequest{
		ResourceName: writeResourceName,
		WriteOffset:  0,
		FinishWrite:  true,
		Data:         compressedData,
	}); err != nil {
		t.Fatalf("Failed to send compressed data: %v", err)
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("Failed to CloseAndRecv: %v", err)
	}
	// CommittedSize should be the size of the uncompressed data (or -1 if blob already exists).
	if got, want := resp.CommittedSize, blobSize; got != want {
		t.Errorf("CommittedSize = %d, want %d", got, want)
	}

	// --- Test Read (Compressed) ---
	// Resource name format: {instance_name}/compressed-blobs/zstd/{uncompressed_hash}/{uncompressed_size}
	readResourceName := fmt.Sprintf("test-instance/compressed-blobs/zstd/%s/%d", d.Hash, d.Size)
	readStream, err := client.Read(ctx, &bspb.ReadRequest{
		ResourceName: readResourceName,
	})
	if err != nil {
		t.Fatalf("Failed to create Read stream: %v", err)
	}

	var readBuf bytes.Buffer
	for {
		chunk, err := readStream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Failed to Recv read chunk: %v", err)
		}
		readBuf.Write(chunk.Data)
	}

	// Verify we got compressed data back
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatalf("Failed to create zstd reader: %v", err)
	}
	decompressedData, err := decoder.DecodeAll(readBuf.Bytes(), nil)
	if err != nil {
		t.Fatalf("Failed to decompress read data: %v", err)
	}

	if !bytes.Equal(decompressedData, blobData) {
		t.Errorf("Read data mismatch after decompression")
	}
}

func TestBatchOperationsZstd(t *testing.T) {
	ctx := t.Context()
	_, casClient := setupTest(ctx, t)

	// Create some blobs
	blobs := [][]byte{
		[]byte("blob1-zstd"),
		[]byte("blob2-zstd"),
	}
	digests := make([]*repb.Digest, len(blobs))
	for i, b := range blobs {
		digests[i] = digest.FromBlob(b).ToProto()
	}

	encoder, _ := zstd.NewWriter(nil)

	// 1. Test BatchUpdateBlobs with Zstd
	updateReqs := []*repb.BatchUpdateBlobsRequest_Request{
		{
			Digest:     digests[0],
			Data:       encoder.EncodeAll(blobs[0], nil),
			Compressor: repb.Compressor_ZSTD,
		},
		{
			Digest:     digests[1],
			Data:       encoder.EncodeAll(blobs[1], nil),
			Compressor: repb.Compressor_ZSTD,
		},
	}
	updateResp, err := casClient.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		Requests: updateReqs,
	})
	if err != nil {
		t.Fatalf("BatchUpdateBlobs failed: %v", err)
	}
	for _, r := range updateResp.Responses {
		if r.Status.Code != 0 {
			t.Errorf("BatchUpdateBlobs response error for %s: %v", r.Digest.Hash, r.Status)
		}
	}

	// 2. Test BatchReadBlobs with Zstd requested
	readResp, err := casClient.BatchReadBlobs(ctx, &repb.BatchReadBlobsRequest{
		Digests:               digests,
		AcceptableCompressors: []repb.Compressor_Value{repb.Compressor_ZSTD},
	})
	if err != nil {
		t.Fatalf("BatchReadBlobs failed: %v", err)
	}
	if len(readResp.Responses) != 2 {
		t.Fatalf("Expected 2 responses, got %d", len(readResp.Responses))
	}

	decoder, _ := zstd.NewReader(nil)

	for i, r := range readResp.Responses {
		if got, want := codes.Code(r.Status.Code), codes.OK; got != want {
			t.Errorf("Response code for blob %d got %v, want %v", i, got, want)
		}

		// Check compressor field
		if got, want := r.Compressor, repb.Compressor_ZSTD; got != want {
			t.Errorf("Compressor for blob %d got %v, want %v", i, got, want)
		}

		// Decompress and verify data
		decompressed, err := decoder.DecodeAll(r.Data, nil)
		if err != nil {
			t.Errorf("Failed to decompress blob %d: %v", i, err)
		}
		if got, want := decompressed, blobs[i]; !bytes.Equal(got, want) {
			t.Errorf("Data for blob %d got %v, want %v", i, got, want)
		}
	}
}

func TestWriteAlreadyExistingBlob(t *testing.T) {
	ctx := t.Context()
	client, _ := setupTest(ctx, t)

	// Generate a 20MB random blob.
	blobSize := int64(20 * 1024 * 1024)
	blobData := make([]byte, blobSize)
	if _, err := rand.Read(blobData); err != nil {
		t.Fatalf("Failed to generate random data: %v", err)
	}
	d := digest.FromBlob(blobData)

	// Upload the blob for the first time.
	uploadID := uuid.New()
	resourceName := fmt.Sprintf("test-instance/uploads/%s/blobs/%s/%d", uploadID, d.Hash, d.Size)
	stream, err := client.Write(ctx)
	if err != nil {
		t.Fatalf("Failed to create Write stream: %v", err)
	}
	chunkSize := int64(4096)
	for offset := int64(0); offset < blobSize; offset += chunkSize {
		end := min(offset+chunkSize, blobSize)
		if err := stream.Send(&bspb.WriteRequest{
			ResourceName: resourceName,
			WriteOffset:  offset,
			FinishWrite:  end == blobSize,
			Data:         blobData[offset:end],
		}); err != nil {
			t.Fatalf("Failed to send data: %v", err)
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("First upload failed: %v", err)
	}
	if got, want := resp.CommittedSize, blobSize; got != want {
		t.Errorf("First upload: CommittedSize = %d, want %d", got, want)
	}

	// Upload the same blob again in 4KB chunks. Per the REAPI spec, the server
	// should close the stream immediately when the blob already exists. Due to
	// transport buffering and Go routine scheduling, we will not immediately get
	// notified that the server closed the stream, so gRPC will allow us to send
	// a few more messages afterwards that will simply be discarded on the server
	// side. However, with a 20MB blob, we expect Send to fail with io.EOF well
	// before all chunks are sent, proving that the server did not consume the
	// entire upload. In practice, we're able to send around ~50 chunks out of
	// total ~5000 before our stream.Send() fails with io.EOF.
	uploadID = uuid.New()
	resourceName = fmt.Sprintf("test-instance/uploads/%s/blobs/%s/%d", uploadID, d.Hash, d.Size)
	stream, err = client.Write(ctx)
	if err != nil {
		t.Fatalf("Failed to create second Write stream: %v", err)
	}
	totalChunks := (blobSize + chunkSize - 1) / chunkSize
	sentChunks := int64(0)
	for offset := int64(0); offset < blobSize; offset += chunkSize {
		end := min(offset+chunkSize, blobSize)
		if err := stream.Send(&bspb.WriteRequest{
			ResourceName: resourceName,
			WriteOffset:  offset,
			FinishWrite:  end == blobSize,
			Data:         blobData[offset:end],
		}); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("Second upload failed (expected io.EOF): %v", err)
			}
			break
		}
		sentChunks++
	}
	t.Logf("Sent %d out of %d chunks", sentChunks, totalChunks)
	if sentChunks == totalChunks {
		t.Errorf("Server did not close stream early: all %d chunks were sent", totalChunks)
	}
	resp, err = stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("Second upload failed (expected success): %v", err)
	}
	if got, want := resp.CommittedSize, blobSize; got != want {
		t.Errorf("Second upload: CommittedSize = %d, want %d", got, want)
	}

	// Compress the data for the third upload.
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("Failed to create zstd writer: %v", err)
	}
	compressedData := encoder.EncodeAll(blobData, nil)
	compressedSize := int64(len(compressedData))

	// Upload the same blob again using compression in 4KB chunks. Per the REAPI
	// spec, the server should close the stream immediately when the blob already
	// exists. With a 20MB blob, we expect Send to fail with io.EOF well before
	// all chunks are sent, proving that the server did not consume the entire
	// upload. The committed_size should be -1 for compressed uploads.
	uploadID = uuid.New()
	resourceName = fmt.Sprintf("test-instance/uploads/%s/compressed-blobs/zstd/%s/%d", uploadID, d.Hash, d.Size)
	stream, err = client.Write(ctx)
	if err != nil {
		t.Fatalf("Failed to create second Write stream: %v", err)
	}
	totalChunks = (compressedSize + chunkSize - 1) / chunkSize
	sentChunks = int64(0)
	for offset := int64(0); offset < compressedSize; offset += chunkSize {
		end := min(offset+chunkSize, compressedSize)
		if err := stream.Send(&bspb.WriteRequest{
			ResourceName: resourceName,
			WriteOffset:  offset,
			FinishWrite:  end == compressedSize,
			Data:         compressedData[offset:end],
		}); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("Second upload failed (expected io.EOF): %v", err)
			}
			break
		}
		sentChunks++
	}
	t.Logf("Sent %d out of %d chunks", sentChunks, totalChunks)
	if sentChunks == totalChunks {
		t.Errorf("Server did not close stream early: all %d chunks were sent", totalChunks)
	}
	resp, err = stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("Third upload failed: %v", err)
	}
	if got, want := resp.CommittedSize, int64(-1); got != want {
		t.Errorf("Third upload: CommittedSize = %d, want %d", got, want)
	}
}

func TestWriteConcurrentUpload(t *testing.T) {
	ctx := t.Context()
	client, _ := setupTest(ctx, t)

	// Create a blob that we'll upload via two concurrent streams.
	blobData := make([]byte, 300)
	if _, err := rand.Read(blobData); err != nil {
		t.Fatalf("Failed to generate random data: %v", err)
	}
	d := digest.FromBlob(blobData)
	chunkSize := 100

	// Start stream A and send only the first chunk.
	uploadIDA := uuid.New()
	resourceNameA := fmt.Sprintf("test-instance/uploads/%s/blobs/%s/%d", uploadIDA, d.Hash, d.Size)
	streamA, err := client.Write(ctx)
	if err != nil {
		t.Fatalf("Failed to create Write stream A: %v", err)
	}
	if err := streamA.Send(&bspb.WriteRequest{
		ResourceName: resourceNameA,
		WriteOffset:  0,
		FinishWrite:  false,
		Data:         blobData[:chunkSize],
	}); err != nil {
		t.Fatalf("Stream A: failed to send first chunk: %v", err)
	}

	// Complete stream B - upload the entire blob in one shot.
	// After this completes, the blob is in the CAS.
	uploadIDB := uuid.New()
	resourceNameB := fmt.Sprintf("test-instance/uploads/%s/blobs/%s/%d", uploadIDB, d.Hash, d.Size)
	streamB, err := client.Write(ctx)
	if err != nil {
		t.Fatalf("Failed to create Write stream B: %v", err)
	}
	if err := streamB.Send(&bspb.WriteRequest{
		ResourceName: resourceNameB,
		WriteOffset:  0,
		FinishWrite:  true,
		Data:         blobData,
	}); err != nil {
		t.Fatalf("Stream B: failed to send data: %v", err)
	}
	respB, err := streamB.CloseAndRecv()
	if err != nil {
		t.Fatalf("Stream B: CloseAndRecv failed: %v", err)
	}
	if got, want := respB.CommittedSize, d.Size; got != want {
		t.Errorf("Stream B: CommittedSize = %d, want %d", got, want)
	}

	// Now send the next chunk on stream A. The server should detect that the blob
	// was already uploaded by stream B and terminate the upload immediately.
	if err := streamA.Send(&bspb.WriteRequest{
		WriteOffset: int64(chunkSize),
		FinishWrite: false,
		Data:        blobData[chunkSize : 2*chunkSize],
	}); err != nil {
		// Send may fail if the server already closed the stream, which is acceptable.
		t.Logf("Stream A: send after concurrent completion returned (expected): %v", err)
	}
	respA, err := streamA.CloseAndRecv()
	if err != nil {
		t.Fatalf("Stream A: CloseAndRecv failed (expected success): %v", err)
	}
	// Per REAPI spec: for an uncompressed upload, committed_size should be the
	// full size of the blob when another client already completed the upload.
	if got, want := respA.CommittedSize, d.Size; got != want {
		t.Errorf("Stream A: CommittedSize = %d, want %d", got, want)
	}
}

func TestWriteWrongDigest(t *testing.T) {
	ctx := t.Context()
	client, _ := setupTest(ctx, t)

	// Create two blobs of the same size but with different content,
	// so only the hash differs (not the size).
	blobData := bytes.Repeat([]byte("a"), 100)
	wrongData := bytes.Repeat([]byte("b"), 100)
	wrongDigest := digest.FromBlob(wrongData)

	// Upload blobData but claim it has wrongDigest's hash.
	uploadID := uuid.New()
	resourceName := fmt.Sprintf("test-instance/uploads/%s/blobs/%s/%d", uploadID, wrongDigest.Hash, wrongDigest.Size)
	stream, err := client.Write(ctx)
	if err != nil {
		t.Fatalf("Failed to create Write stream: %v", err)
	}
	if err := stream.Send(&bspb.WriteRequest{
		ResourceName: resourceName,
		WriteOffset:  0,
		FinishWrite:  true,
		Data:         blobData,
	}); err != nil {
		t.Fatalf("Failed to send data: %v", err)
	}
	_, err = stream.CloseAndRecv()
	if err == nil {
		t.Fatal("Expected INVALID_ARGUMENT error, got nil")
	}
	// Per REAPI spec: if the digest does not match, an INVALID_ARGUMENT error is returned.
	if got, want := status.Code(err), codes.InvalidArgument; got != want {
		t.Errorf("Error code = %v, want %v (error: %v)", got, want, err)
	}
}
