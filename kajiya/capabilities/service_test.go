// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package capabilities

import (
	"context"
	"net"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/testing/protocmp"

	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
	semverpb "go.chromium.org/build/remote-apis/build/bazel/semver"

	"go.chromium.org/build/kajiya/server"
)

// bufferSize is the size of the in-memory buffer.
const bufferSize = 1024 * 1024

// startTestServer sets up a gRPC server listening on a bufconn listener.
// It returns the listener (to dial to) and a cleanup function.
func startTestServer(t *testing.T) *bufconn.Listener {
	t.Helper()

	// Create an in-memory listener
	lis := bufconn.Listen(bufferSize)

	// Create a standard gRPC server
	cfg := server.Config{MaxBatchTotalSizeBytes: 1048576}
	s := grpc.NewServer(grpc.MaxRecvMsgSize(cfg.RecommendedMaxRecvMsgSize()))

	// Register the service implementation
	Register(s, cfg)

	// Start serving in a background goroutine
	go func() {
		if err := s.Serve(lis); err != nil {
			t.Errorf("Failed to serve gRPC server: %v", err)
		}
	}()

	t.Cleanup(func() {
		s.Stop()
		err := lis.Close()
		if err != nil {
			t.Errorf("Failed to close bufnet listener: %v", err)
		}
	})

	return lis
}

func TestGetCapabilities(t *testing.T) {
	// Start the server.
	lis := startTestServer(t)

	// Create a client that dials the in-memory listener.
	ctx := t.Context()
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("Failed to dial bufnet: %v", err)
	}
	defer func(conn *grpc.ClientConn) {
		if err := conn.Close(); err != nil {
			t.Errorf("Failed to close gRPC client connection: %v", err)
		}
	}(conn)

	// Create the actual protobuf client.
	client := repb.NewCapabilitiesClient(conn)

	// Call the GetCapabilities RPC.
	req := &repb.GetCapabilitiesRequest{
		InstanceName: "test-instance",
	}
	resp, err := client.GetCapabilities(ctx, req)
	if err != nil {
		t.Fatalf("GetCapabilities RPC failed: %v", err)
	}
	if resp == nil {
		t.Fatal("GetCapabilities returned nil response")
	}

	want := &repb.ServerCapabilities{
		LowApiVersion:  &semverpb.SemVer{Major: 2, Minor: 0},
		HighApiVersion: &semverpb.SemVer{Major: 2, Minor: 0},
		CacheCapabilities: &repb.CacheCapabilities{
			DigestFunctions: []repb.DigestFunction_Value{repb.DigestFunction_SHA256},
			ActionCacheUpdateCapabilities: &repb.ActionCacheUpdateCapabilities{
				UpdateEnabled: true,
			},
			CachePriorityCapabilities: &repb.PriorityCapabilities{
				Priorities: []*repb.PriorityCapabilities_PriorityRange{
					{
						MinPriority: 0,
						MaxPriority: 0,
					},
				},
			},
			MaxBatchTotalSizeBytes:      1048576,
			SymlinkAbsolutePathStrategy: repb.SymlinkAbsolutePathStrategy_DISALLOWED,
			SupportedCompressors:        []repb.Compressor_Value{repb.Compressor_IDENTITY, repb.Compressor_ZSTD},
		},
		ExecutionCapabilities: &repb.ExecutionCapabilities{
			DigestFunction: repb.DigestFunction_SHA256,
			ExecEnabled:    true,
			ExecutionPriorityCapabilities: &repb.PriorityCapabilities{
				Priorities: []*repb.PriorityCapabilities_PriorityRange{
					{
						MinPriority: 0,
						MaxPriority: 0,
					},
				},
			},
			DigestFunctions: []repb.DigestFunction_Value{repb.DigestFunction_SHA256},
		},
	}

	if diff := cmp.Diff(want, resp, protocmp.Transform()); diff != "" {
		t.Errorf("GetCapabilities() mismatch (-want +got):\n%s", diff)
	}
}
