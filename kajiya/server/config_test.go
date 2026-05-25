// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package server

import (
	"context"
	"net"
	"testing"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/digest"
)

const defaultMaxRecvMsgSize = 4 * 1024 * 1024 // 4 MiB (gRPC default)

func TestRecommendedMaxRecvMsgSize_Default(t *testing.T) {
	cfg := Config{MaxBatchTotalSizeBytes: 0}
	if got, want := cfg.RecommendedMaxRecvMsgSize(), defaultMaxRecvMsgSize; got != want {
		t.Errorf("RecommendedMaxRecvMsgSize() = %d, want %d", got, want)
	}
}

func TestRecommendedMaxRecvMsgSize_SmallValue(t *testing.T) {
	// A small MaxBatchTotalSizeBytes should still return at least the gRPC default.
	cfg := Config{MaxBatchTotalSizeBytes: 1024}
	if got, want := cfg.RecommendedMaxRecvMsgSize(), defaultMaxRecvMsgSize; got < want {
		t.Errorf("RecommendedMaxRecvMsgSize() = %d, want >= %d", got, defaultMaxRecvMsgSize)
	}
}

func TestRecommendedMaxRecvMsgSize_LargeValue(t *testing.T) {
	cfg := Config{MaxBatchTotalSizeBytes: 100 * 1024 * 1024}
	if got, want := cfg.RecommendedMaxRecvMsgSize(), 100*1024*1024; got <= want {
		t.Errorf("RecommendedMaxRecvMsgSize() = %d, want > %d", got, 100*1024*1024)
	}
}

// TestRecommendedMaxRecvMsgSize_MatchesProtoMarshal verifies that RecommendedMaxRecvMsgSize
// is large enough to hold the actual proto.Marshal output for various
// MaxBatchTotalSizeBytes values.
func TestRecommendedMaxRecvMsgSize_MatchesProtoMarshal(t *testing.T) {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	defer enc.Close()

	for _, blobSize := range []int64{1, 100, 4096, 1024 * 1024, 10 * 1024 * 1024} {
		maxDataSize := int64(enc.MaxEncodedSize(int(blobSize)))
		dummyData := make([]byte, maxDataSize)

		// Construct and marshal a BatchUpdateBlobsRequest.
		batchUpdateReq := &repb.BatchUpdateBlobsRequest{
			Requests: []*repb.BatchUpdateBlobsRequest_Request{
				{
					Digest: &repb.Digest{
						Hash:      digest.Empty.Hash,
						SizeBytes: maxDataSize,
					},
					Data:       dummyData,
					Compressor: repb.Compressor_ZSTD,
				},
			},
			DigestFunction: repb.DigestFunction_SHA256,
		}
		updateBytes, err := proto.Marshal(batchUpdateReq)
		if err != nil {
			t.Fatalf("proto.Marshal(BatchUpdateBlobsRequest): %v", err)
		}

		cfg := Config{MaxBatchTotalSizeBytes: blobSize}
		computed := cfg.RecommendedMaxRecvMsgSize()

		if computed < len(updateBytes) {
			t.Errorf("MaxBatchTotalSizeBytes=%d: RecommendedMaxRecvMsgSize()=%d < actual marshaled size=%d",
				blobSize, computed, len(updateBytes))
		} else {
			t.Logf("MaxBatchTotalSizeBytes=%d: RecommendedMaxRecvMsgSize()=%d, actual marshaled size=%d (delta=%d)",
				blobSize, computed, len(updateBytes), computed-len(updateBytes))
		}
	}
}

// TestRecommendedMaxRecvMsgSize_Integration verifies that a real gRPC
// server configured with RecommendedMaxRecvMsgSize accepts worst-case
// messages at the limit and rejects messages that exceed it.
func TestRecommendedMaxRecvMsgSize_Integration(t *testing.T) {
	const batchLimit = 10 * 1024 * 1024 // 10 MiB — exceeds the 4 MiB gRPC default.

	cfg := Config{MaxBatchTotalSizeBytes: batchLimit}

	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	defer enc.Close()

	maxDataSize := enc.MaxEncodedSize(int(batchLimit))

	// Start a gRPC server with the recommended max receive message size.
	lis := bufconn.Listen(64 * 1024 * 1024)
	srv := grpc.NewServer(grpc.MaxRecvMsgSize(cfg.RecommendedMaxRecvMsgSize()))
	repb.RegisterContentAddressableStorageServer(srv, &repb.UnimplementedContentAddressableStorageServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		lis.Close()
	})

	// Create a client connection to the in-memory listener.
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	client := repb.NewContentAddressableStorageClient(conn)
	ctx := t.Context()

	buildRequest := func(dataSize int) *repb.BatchUpdateBlobsRequest {
		data := make([]byte, dataSize)
		return &repb.BatchUpdateBlobsRequest{
			Requests: []*repb.BatchUpdateBlobsRequest_Request{
				{
					Digest:     digest.FromBlob(data).ToProto(),
					Data:       data,
					Compressor: repb.Compressor_ZSTD,
				},
			},
			DigestFunction: repb.DigestFunction_SHA256,
		}
	}

	// At-limit: the server should accept the message (returning Unimplemented
	// because we registered the stub service, not ResourceExhausted).
	t.Run("at_limit", func(t *testing.T) {
		_, err := client.BatchUpdateBlobs(ctx, buildRequest(maxDataSize))
		if got, want := grpcstatus.Code(err), codes.Unimplemented; got != want {
			t.Errorf("at-limit request: got code %v, want %v (err: %v)", got, want, err)
		}
	})

	// Over-limit: the server should reject the message with ResourceExhausted.
	t.Run("over_limit", func(t *testing.T) {
		_, err := client.BatchUpdateBlobs(ctx, buildRequest(maxDataSize+1))
		if got, want := grpcstatus.Code(err), codes.ResourceExhausted; got != want {
			t.Errorf("over-limit request: got code %v, want %v (err: %v)", got, want, err)
		}
	})
}
