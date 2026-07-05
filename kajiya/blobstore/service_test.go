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

	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/server"
)

// bufferSize is the size of the in-memory buffer.
const bufferSize = 1024 * 1024

var (
	encoder *zstd.Encoder
	decoder *zstd.Decoder
)

// startTestServer sets up a gRPC server listening on a bufconn listener.
// It returns the listener (to dial to) and a cleanup function.
func startTestServer(t testing.TB, cas *ContentAddressableStorage, cfg server.Config) *bufconn.Listener {
	t.Helper()

	// Create an in-memory listener
	lis := bufconn.Listen(bufferSize)

	// Create a standard gRPC server
	s := grpc.NewServer(grpc.MaxRecvMsgSize(cfg.RecommendedMaxRecvMsgSize()))

	// Register the service implementation
	Register(s, cas, cfg)

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

func setupTest(ctx context.Context, t testing.TB, cfg server.Config) (bspb.ByteStreamClient, repb.ContentAddressableStorageClient) {
	t.Helper()

	// Setup CAS.
	dataDir := t.TempDir()
	cas, err := New(ctx, dataDir)
	if err != nil {
		t.Fatalf("Failed to create CAS: %v", err)
	}

	// Start the server
	lis := startTestServer(t, cas, cfg)

	// Create a client that dials the in-memory listener
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(cfg.RecommendedMaxRecvMsgSize())),
	)
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Create zstd encoder and decoder
	encoder, err = zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("Failed to create zstd encoder: %v", err)
	}
	decoder, err = zstd.NewReader(nil)
	if err != nil {
		t.Fatalf("Failed to create zstd decoder: %v", err)
	}
	t.Cleanup(func() {
		if err := encoder.Close(); err != nil {
			t.Fatalf("Failed to close zstd encoder: %v", err)
		}
		decoder.Close()
	})

	return bspb.NewByteStreamClient(conn), repb.NewContentAddressableStorageClient(conn)
}

func randomBlob(t testing.TB, size int) ([]byte, *repb.Digest) {
	blobData := make([]byte, size)
	if _, err := rand.Read(blobData); err != nil {
		t.Fatalf("Failed to generate random data: %v", err)
	}
	d := digest.SHA256.FromBytes(blobData).Proto()
	return blobData, d
}

func randomBlobs(t testing.TB, size, num int) ([][]byte, []*repb.Digest) {
	blobs := make([][]byte, num)
	digests := make([]*repb.Digest, num)
	for i := range num {
		blobs[i], digests[i] = randomBlob(t, size)
	}
	return blobs, digests
}

// TestReadWrite verifies basic ByteStream Write and Read of a 5 MB blob,
// ensuring chunked uploads and downloads round-trip correctly.
func TestReadWrite(t *testing.T) {
	ctx := t.Context()
	client, _ := setupTest(ctx, t, server.Config{})

	// Generate random data larger than maxChunkSize (5 MiB) to force chunking
	blobData, d := randomBlob(t, 5*1024*1024)

	// --- Test Write ---
	uploadID := uuid.New()
	writeResourceName := fmt.Sprintf("test-instance/uploads/%s/blobs/%s/%d", uploadID, d.Hash, d.SizeBytes)

	stream, err := client.Write(ctx)
	if err != nil {
		t.Fatalf("Failed to create Write stream: %v", err)
	}

	// Send data in 1MB chunks
	chunkSize := int64(1024 * 1024)
	offset := int64(0)
	for offset < d.SizeBytes {
		end := min(offset+chunkSize, d.SizeBytes)
		req := &bspb.WriteRequest{
			ResourceName: writeResourceName,
			WriteOffset:  offset,
			FinishWrite:  end == d.SizeBytes,
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
	if got, want := resp.CommittedSize, d.SizeBytes; got != want {
		t.Errorf("CommittedSize = %d, want %d", got, want)
	}

	// --- Test Read ---
	readResourceName := fmt.Sprintf("test-instance/blobs/%s/%d", d.Hash, d.SizeBytes)
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

// TestReadWriteZstd verifies ByteStream Write and Read with zstd compression,
// ensuring that compressed uploads are stored correctly and can be read back
// as compressed data that decompresses to the original content.
func TestReadWriteZstd(t *testing.T) {
	ctx := t.Context()
	client, _ := setupTest(ctx, t, server.Config{})

	// Generate random data
	blobData, d := randomBlob(t, 1*1024*1024)

	// --- Test Write (Compressed) ---
	uploadID := uuid.New()
	// Resource name format: {instance_name}/uploads/{uuid}/compressed-blobs/zstd/{uncompressed_hash}/{uncompressed_size}
	writeResourceName := fmt.Sprintf("test-instance/uploads/%s/compressed-blobs/zstd/%s/%d", uploadID, d.Hash, d.SizeBytes)

	stream, err := client.Write(ctx)
	if err != nil {
		t.Fatalf("Failed to create Write stream: %v", err)
	}

	// Send compressed data
	if err := stream.Send(&bspb.WriteRequest{
		ResourceName: writeResourceName,
		WriteOffset:  0,
		FinishWrite:  true,
		Data:         encoder.EncodeAll(blobData, nil),
	}); err != nil {
		t.Fatalf("Failed to send compressed data: %v", err)
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("Failed to CloseAndRecv: %v", err)
	}
	// CommittedSize should be the size of the uncompressed data (or -1 if blob already exists).
	if got, want := resp.CommittedSize, d.SizeBytes; got != want {
		t.Errorf("CommittedSize = %d, want %d", got, want)
	}

	// --- Test Read (Compressed) ---
	// Resource name format: {instance_name}/compressed-blobs/zstd/{uncompressed_hash}/{uncompressed_size}
	readResourceName := fmt.Sprintf("test-instance/compressed-blobs/zstd/%s/%d", d.Hash, d.SizeBytes)
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
	decompressedData, err := decoder.DecodeAll(readBuf.Bytes(), nil)
	if err != nil {
		t.Fatalf("Failed to decompress read data: %v", err)
	}

	if !bytes.Equal(decompressedData, blobData) {
		t.Errorf("Read data mismatch after decompression")
	}
}

// TestWriteAlreadyExistingBlob verifies that re-uploading a blob that already
// exists in the CAS causes the server to close the stream early, both for
// uncompressed and zstd-compressed uploads. This proves the server avoids
// consuming redundant data.
func TestWriteAlreadyExistingBlob(t *testing.T) {
	ctx := t.Context()
	client, _ := setupTest(ctx, t, server.Config{})

	// Generate a 20MB random blob.
	blobData, d := randomBlob(t, 20*1024*1024)

	// Upload the blob for the first time.
	uploadID := uuid.New()
	resourceName := fmt.Sprintf("test-instance/uploads/%s/blobs/%s/%d", uploadID, d.Hash, d.SizeBytes)
	stream, err := client.Write(ctx)
	if err != nil {
		t.Fatalf("Failed to create Write stream: %v", err)
	}
	chunkSize := int64(4096)
	for offset := int64(0); offset < d.SizeBytes; offset += chunkSize {
		end := min(offset+chunkSize, d.SizeBytes)
		if err := stream.Send(&bspb.WriteRequest{
			ResourceName: resourceName,
			WriteOffset:  offset,
			FinishWrite:  end == d.SizeBytes,
			Data:         blobData[offset:end],
		}); err != nil {
			t.Fatalf("Failed to send data: %v", err)
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("First upload failed: %v", err)
	}
	if got, want := resp.CommittedSize, d.SizeBytes; got != want {
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
	resourceName = fmt.Sprintf("test-instance/uploads/%s/blobs/%s/%d", uploadID, d.Hash, d.SizeBytes)
	stream, err = client.Write(ctx)
	if err != nil {
		t.Fatalf("Failed to create second Write stream: %v", err)
	}
	totalChunks := (d.SizeBytes + chunkSize - 1) / chunkSize
	sentChunks := int64(0)
	for offset := int64(0); offset < d.SizeBytes; offset += chunkSize {
		end := min(offset+chunkSize, d.SizeBytes)
		if err := stream.Send(&bspb.WriteRequest{
			ResourceName: resourceName,
			WriteOffset:  offset,
			FinishWrite:  end == d.SizeBytes,
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
	if got, want := resp.CommittedSize, d.SizeBytes; got != want {
		t.Errorf("Second upload: CommittedSize = %d, want %d", got, want)
	}

	// Compress the data for the third upload.
	compressedData := encoder.EncodeAll(blobData, nil)
	compressedSize := int64(len(compressedData))

	// Upload the same blob again using compression in 4KB chunks. Per the REAPI
	// spec, the server should close the stream immediately when the blob already
	// exists. With a 20MB blob, we expect Send to fail with io.EOF well before
	// all chunks are sent, proving that the server did not consume the entire
	// upload. The committed_size should be -1 for compressed uploads.
	uploadID = uuid.New()
	resourceName = fmt.Sprintf("test-instance/uploads/%s/compressed-blobs/zstd/%s/%d", uploadID, d.Hash, d.SizeBytes)
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

// TestWriteConcurrentUpload verifies that when two streams upload the same blob
// concurrently, the server correctly handles the race: the first stream to
// complete wins, and the other stream is terminated gracefully with the correct
// committed_size.
func TestWriteConcurrentUpload(t *testing.T) {
	ctx := t.Context()
	client, _ := setupTest(ctx, t, server.Config{})

	// Create a blob that we'll upload via two concurrent streams.
	blobData, d := randomBlob(t, 300)
	chunkSize := 100

	// Start stream A and send only the first chunk.
	uploadIDA := uuid.New()
	resourceNameA := fmt.Sprintf("test-instance/uploads/%s/blobs/%s/%d", uploadIDA, d.Hash, d.SizeBytes)
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
	resourceNameB := fmt.Sprintf("test-instance/uploads/%s/blobs/%s/%d", uploadIDB, d.Hash, d.SizeBytes)
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
	if got, want := respB.CommittedSize, d.SizeBytes; got != want {
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
	// Even though we have not uploaded the full blob and set FinishWrite to
	// true in this stream, we expect a successful response due to the other
	// stream completing the upload.
	respA, err := streamA.CloseAndRecv()
	if err != nil {
		t.Fatalf("Stream A: CloseAndRecv failed (expected success): %v", err)
	}
	// Per REAPI spec: for an uncompressed upload, committed_size should be the
	// full size of the blob when another client already completed the upload.
	if got, want := respA.CommittedSize, d.SizeBytes; got != want {
		t.Errorf("Stream A: CommittedSize = %d, want %d", got, want)
	}
}

// TestWriteWrongDigest verifies that uploading data whose content does not
// match the declared digest results in an INVALID_ARGUMENT error per the REAPI
// spec.
func TestWriteWrongDigest(t *testing.T) {
	ctx := t.Context()
	client, _ := setupTest(ctx, t, server.Config{})

	// Create two blobs of the same size but with different content,
	// so only the hash differs (not the size).
	blobData := bytes.Repeat([]byte("a"), 100)
	wrongData := bytes.Repeat([]byte("b"), 100)
	wrongDigest := digest.SHA256.FromBytes(wrongData)

	// Upload blobData but claim it has wrongDigest's hash.
	uploadID := uuid.New()
	resourceName := fmt.Sprintf("test-instance/uploads/%s/blobs/%s/%d", uploadID, wrongDigest.Hash, wrongDigest.SizeBytes)
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

// TestBatchOperations exercises FindMissingBlobs, BatchUpdateBlobs, and
// BatchReadBlobs in sequence, verifying that blobs can be uploaded, discovered,
// and read back correctly, and that missing blobs are reported as NOT_FOUND.
func TestBatchOperations(t *testing.T) {
	ctx := t.Context()
	_, casClient := setupTest(ctx, t, server.Config{MaxBatchTotalSizeBytes: 3000})

	// Create some blobs
	blobs, digests := randomBlobs(t, 1000, 4)

	// 1. Test FindMissingBlobs - all should be missing
	missingResp, err := casClient.FindMissingBlobs(ctx, &repb.FindMissingBlobsRequest{
		BlobDigests: digests,
	})
	if err != nil {
		t.Fatalf("FindMissingBlobs failed: %v", err)
	}
	if got, want := len(missingResp.MissingBlobDigests), len(digests); got != want {
		t.Errorf("Got %d missing blobs, want %d", got, want)
	}

	// 2. Test BatchUpdateBlobs - upload all four (this should fail due to batch size limit)
	_, err = casClient.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		Requests: []*repb.BatchUpdateBlobsRequest_Request{
			{Digest: digests[0], Data: blobs[0]},
			{Digest: digests[1], Data: blobs[1]},
			{Digest: digests[2], Data: blobs[2]},
			{Digest: digests[3], Data: blobs[3]},
		},
		DigestFunction: repb.DigestFunction_SHA256,
	})
	if err == nil {
		t.Fatalf("BatchUpdateBlobs succeeded, wanted INVALID_ARGUMENT")
	}
	if got, want := status.Code(err), codes.InvalidArgument; got != want {
		t.Errorf("Error code = %v, want %v (error: %v)", got, want, err)
	}

	// 2b. Test BatchUpdateBlobs - upload the first three (this should succeed)
	updateResp, err := casClient.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		Requests: []*repb.BatchUpdateBlobsRequest_Request{
			{Digest: digests[0], Data: blobs[0]},
			{Digest: digests[1], Data: blobs[1]},
			{Digest: digests[2], Data: blobs[2]},
		},
		DigestFunction: repb.DigestFunction_SHA256,
	})
	if err != nil {
		t.Fatalf("BatchUpdateBlobs failed: %v", err)
	}
	for _, r := range updateResp.Responses {
		if r.Status.Code != 0 {
			t.Errorf("BatchUpdateBlobs response error for %s: %v", r.Digest.Hash, r.Status)
		}
	}

	// 3. Test FindMissingBlobs again - only the fourth should be missing
	missingResp, err = casClient.FindMissingBlobs(ctx, &repb.FindMissingBlobsRequest{
		BlobDigests: digests,
	})
	if err != nil {
		t.Fatalf("FindMissingBlobs failed: %v", err)
	}
	if got, want := len(missingResp.MissingBlobDigests), 1; got != want {
		t.Errorf("Got %d missing blobs, want %d", got, want)
	}
	if got, want := missingResp.MissingBlobDigests[0].Hash, digests[3].Hash; got != want {
		t.Errorf("Hash of missing blob is %v, want %v", got, want)
	}

	// 4. Test BatchReadBlobs - read all four (this should fail due to batch size limit)
	_, err = casClient.BatchReadBlobs(ctx, &repb.BatchReadBlobsRequest{
		Digests: digests,
	})
	if err == nil {
		t.Fatalf("BatchReadBlobs succeeded, wanted INVALID_ARGUMENT")
	}
	if got, want := status.Code(err), codes.InvalidArgument; got != want {
		t.Errorf("Error code = %v, want %v (error: %v)", got, want, err)
	}

	// 4b. Test BatchReadBlobs - read the first two and the last one (this should succeed)
	readResp, err := casClient.BatchReadBlobs(ctx, &repb.BatchReadBlobsRequest{
		Digests: []*repb.Digest{digests[0], digests[1], digests[3]},
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
				t.Errorf("Data for blob %d does not match uploaded data", i)
			}
		} else {
			if got, want := codes.Code(r.Status.Code), codes.NotFound; got != want {
				t.Errorf("Response code for blob %d got %v, want %v", i, got, want)
			}
		}
	}
}

// TestBatchOperationsZstd verifies BatchUpdateBlobs and BatchReadBlobs with
// zstd compression, ensuring that compressed batch uploads are stored correctly
// and can be read back with the zstd compressor field set in the response.
func TestBatchOperationsZstd(t *testing.T) {
	ctx := t.Context()
	_, casClient := setupTest(ctx, t, server.Config{})

	// Create some blobs
	blobs, digests := randomBlobs(t, 4096, 2)

	// 1. Test BatchUpdateBlobs with zstd
	updateResp, err := casClient.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		Requests: []*repb.BatchUpdateBlobsRequest_Request{{
			Digest:     digests[0],
			Data:       encoder.EncodeAll(blobs[0], nil),
			Compressor: repb.Compressor_ZSTD,
		}, {
			Digest:     digests[1],
			Data:       encoder.EncodeAll(blobs[1], nil),
			Compressor: repb.Compressor_ZSTD,
		}},
		DigestFunction: repb.DigestFunction_SHA256,
	})
	if err != nil {
		t.Fatalf("BatchUpdateBlobs failed: %v", err)
	}
	for _, r := range updateResp.Responses {
		if r.Status.Code != 0 {
			t.Errorf("BatchUpdateBlobs response error for %s: %v", r.Digest.Hash, r.Status)
		}
	}

	// 2. Test BatchReadBlobs with zstd requested
	readResp, err := casClient.BatchReadBlobs(ctx, &repb.BatchReadBlobsRequest{
		Digests:               digests,
		AcceptableCompressors: []repb.Compressor_Value{repb.Compressor_ZSTD},
	})
	if err != nil {
		t.Fatalf("BatchReadBlobs failed: %v", err)
	}
	if got, want := len(readResp.Responses), 2; got != want {
		t.Fatalf("Got %d responses, want %d", got, want)
	}

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

// TestBatchOperationsBlobsLargeBlobs verifies that large blobs can be uploaded
// and read back correctly after configuring a large enough MaxBatchTotalSizeBytes.
// It also checks the error codes returned when going over the limit.
func TestBatchOperationsBlobsLargeBlobs(t *testing.T) {
	ctx := t.Context()
	const limit = 10 * 1024 * 1024 // 10 MB
	_, casClient := setupTest(ctx, t, server.Config{MaxBatchTotalSizeBytes: limit})

	// Upload a single large blob exactly at the size limit.
	blobData, d := randomBlob(t, limit)
	_, err := casClient.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		Requests: []*repb.BatchUpdateBlobsRequest_Request{{
			Digest: d,
			Data:   blobData,
		}},
		DigestFunction: repb.DigestFunction_SHA256,
	})
	if err != nil {
		t.Fatalf("Failed to upload blob: %v", err)
	}

	// Cross-check: The upload should fail if we exceed the size limit by even
	// a single byte.
	tooLargeBlobData := append(blobData, 0)
	_, err = casClient.BatchUpdateBlobs(ctx, &repb.BatchUpdateBlobsRequest{
		Requests: []*repb.BatchUpdateBlobsRequest_Request{{
			Digest: digest.SHA256.FromBytes(tooLargeBlobData).Proto(),
			Data:   tooLargeBlobData,
		}},
		DigestFunction: repb.DigestFunction_SHA256,
	})
	if err == nil {
		t.Fatalf("Expected upload to fail due to size limit, but it succeeded")
	}
	if got, want := status.Code(err), codes.InvalidArgument; got != want {
		t.Errorf("Error code = %v, want %v (error: %v)", got, want, err)
	}

	// Verify that we can download our successfully stored blob again.
	readResp, err := casClient.BatchReadBlobs(ctx, &repb.BatchReadBlobsRequest{
		Digests: []*repb.Digest{d},
	})
	if err != nil {
		t.Fatalf("Failed to read blob: %v", err)
	}
	if got, want := len(readResp.Responses), 1; got != want {
		t.Fatalf("Got %d responses, want %d", got, want)
	}
	if got, want := readResp.Responses[0].Status.Code, int32(codes.OK); got != want {
		t.Errorf("Read blob status = %v, want %v", got, want)
	}
	if got, want := readResp.Responses[0].Data, blobData; !bytes.Equal(got, want) {
		t.Errorf("Data for blob does not match uploaded data")
	}
}
