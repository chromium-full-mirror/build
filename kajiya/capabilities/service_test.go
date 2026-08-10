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

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
	semverpb "go.chromium.org/build/remote-apis/build/bazel/semver"

	"go.chromium.org/build/kajiya/server"
)

// bufferSize is the size of the in-memory buffer.
const bufferSize = 1024 * 1024

// startTestServer sets up a gRPC server listening on a bufconn listener.
// It returns the listener (to dial to) and a cleanup function.
func startTestServer(t *testing.T, cfg server.Config) *bufconn.Listener {
	t.Helper()

	// Create an in-memory listener
	lis := bufconn.Listen(bufferSize)

	// Create a standard gRPC server
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

// getCapabilities starts a server with the given config and returns its
// GetCapabilities response.
func getCapabilities(t *testing.T, cfg server.Config) *repb.ServerCapabilities {
	t.Helper()

	lis := startTestServer(t, cfg)

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
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("Failed to close gRPC client connection: %v", err)
		}
	})

	// Create the actual protobuf client and call the GetCapabilities RPC.
	client := repb.NewCapabilitiesClient(conn)
	resp, err := client.GetCapabilities(ctx, &repb.GetCapabilitiesRequest{
		InstanceName: "test-instance",
	})
	if err != nil {
		t.Fatalf("GetCapabilities RPC failed: %v", err)
	}
	if resp == nil {
		t.Fatal("GetCapabilities returned nil response")
	}
	return resp
}

func TestGetCapabilities(t *testing.T) {
	// blake3 is advertised in addition to the default to verify that the
	// configured set (in order, first = default) is returned.
	blake3Fn, err := digest.Lookup(repb.DigestFunction_BLAKE3)
	if err != nil {
		t.Fatal(err)
	}
	resp := getCapabilities(t, server.Config{
		MaxBatchTotalSizeBytes: 1048576,
		DigestFunctions:        []digest.Function{digest.SHA256, blake3Fn},
	})

	advertised := []repb.DigestFunction_Value{repb.DigestFunction_SHA256, repb.DigestFunction_BLAKE3}
	want := &repb.ServerCapabilities{
		LowApiVersion:  &semverpb.SemVer{Major: 2, Minor: 0},
		HighApiVersion: &semverpb.SemVer{Major: 2, Minor: 0},
		CacheCapabilities: &repb.CacheCapabilities{
			DigestFunctions: advertised,
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
			DigestFunctions:         advertised,
			SupportedNodeProperties: []string{"MTime", "UnixMode"},
		},
	}

	if diff := cmp.Diff(want, resp, protocmp.Transform()); diff != "" {
		t.Errorf("GetCapabilities() mismatch (-want +got):\n%s", diff)
	}
}

// TestGetCapabilitiesDefaultSHA256Only verifies that a server without a
// configured digest-function set advertises only SHA-256.
func TestGetCapabilitiesDefaultSHA256Only(t *testing.T) {
	resp := getCapabilities(t, server.Config{})

	wantFns := []repb.DigestFunction_Value{repb.DigestFunction_SHA256}
	if diff := cmp.Diff(wantFns, resp.GetCacheCapabilities().GetDigestFunctions(), protocmp.Transform()); diff != "" {
		t.Errorf("CacheCapabilities.DigestFunctions mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantFns, resp.GetExecutionCapabilities().GetDigestFunctions(), protocmp.Transform()); diff != "" {
		t.Errorf("ExecutionCapabilities.DigestFunctions mismatch (-want +got):\n%s", diff)
	}
	if got, want := resp.GetExecutionCapabilities().GetDigestFunction(), repb.DigestFunction_SHA256; got != want {
		t.Errorf("ExecutionCapabilities.DigestFunction = %v, want %v", got, want)
	}
}
