// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"

	"github.com/google/uuid"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/kajiya/log"
	"go.chromium.org/build/kajiya/server"
)

func setupBenchmark(ctx context.Context, t testing.TB) (bspb.ByteStreamClient, *ContentAddressableStorage) {
	t.Helper()

	// Setup CAS.
	dataDir := t.TempDir()
	cas, err := New(ctx, dataDir)
	if err != nil {
		t.Fatalf("Failed to create CAS: %v", err)
	}

	// Start the server
	lis := startTestServer(t, cas, server.Config{})

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

	return bspb.NewByteStreamClient(conn), cas
}

func BenchmarkUpload(b *testing.B) {
	// Disable info logging in slog for benchmarks.
	slog.SetDefault(slog.New(log.NewPrettyHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})))

	sizes := []struct {
		name string
		size int64
	}{
		{"4KB", 4 * 1024},
		{"128KB", 128 * 1024},
		{"1MB", 1 * 1024 * 1024},
		{"10MB", 10 * 1024 * 1024},
		{"100MB", 100 * 1024 * 1024},
	}

	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			ctx := b.Context()
			client, cas := setupBenchmark(ctx, b)

			// Generate random data once
			blobData := make([]byte, sz.size)
			if _, err := rand.Read(blobData); err != nil {
				b.Fatalf("Failed to generate random data: %v", err)
			}
			d := digest.SHA256.FromBytes(blobData)

			b.ResetTimer()
			b.ReportAllocs()
			b.SetBytes(sz.size)

			for b.Loop() {
				uploadBlob(ctx, b, client, d, blobData)

				// Delete the blob from CAS to ensure the next upload is a fresh write.
				b.StopTimer()
				if err := cas.Delete(digest.SHA256, d); err != nil {
					b.Fatalf("Failed to delete blob: %v", err)
				}
				b.StartTimer()
			}
		})
	}
}

func BenchmarkDownload(b *testing.B) {
	// Disable info logging in slog for benchmarks.
	slog.SetDefault(slog.New(log.NewPrettyHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})))

	sizes := []struct {
		name string
		size int64
	}{
		{"4KB", 4 * 1024},
		{"128KB", 128 * 1024},
		{"1MB", 1 * 1024 * 1024},
		{"10MB", 10 * 1024 * 1024},
		{"100MB", 100 * 1024 * 1024},
	}

	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			ctx := b.Context()
			client, _ := setupTest(ctx, b, server.Config{})

			// Generate random data once
			blobData := make([]byte, sz.size)
			if _, err := rand.Read(blobData); err != nil {
				b.Fatalf("Failed to generate random data: %v", err)
			}
			d := digest.SHA256.FromBytes(blobData)

			// Upload once so we can download it
			uploadBlob(ctx, b, client, d, blobData)

			b.ResetTimer()
			b.ReportAllocs()
			b.SetBytes(sz.size)

			for b.Loop() {
				readResourceName := fmt.Sprintf("test-instance/blobs/%s/%d", d.Hash, d.SizeBytes)
				readStream, err := client.Read(ctx, &bspb.ReadRequest{
					ResourceName: readResourceName,
				})
				if err != nil {
					b.Fatalf("Failed to create Read stream: %v", err)
				}

				// Discard output
				for {
					_, err := readStream.Recv()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						b.Fatalf("Failed to Recv read chunk: %v", err)
					}
				}
			}
		})
	}
}

// BenchmarkPath measures the per-digest cost of computing a blob's on-disk
// path, a hot operation in FindMissingBlobs and FUSE sandbox registration.
func BenchmarkPath(b *testing.B) {
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(oldLogger) })

	cas, err := NewWithOpts(b.Context(), b.TempDir(), Options{Sharded: true})
	if err != nil {
		b.Fatal(err)
	}
	d := digest.SHA256.FromBytes([]byte("hello"))
	b.ReportAllocs()
	for b.Loop() {
		_ = cas.Path(digest.SHA256, d)
	}
}

func uploadBlob(ctx context.Context, t testing.TB, client bspb.ByteStreamClient, d digest.Digest, blobData []byte) {
	t.Helper()

	uploadID := uuid.New()
	writeResourceName := fmt.Sprintf("test-instance/uploads/%s/blobs/%s/%d", uploadID, d.Hash, d.SizeBytes)

	stream, err := client.Write(ctx)
	if err != nil {
		t.Fatalf("Failed to create Write stream: %v", err)
	}

	// Send data in chunks
	chunkSize := int64(1024 * 1024)
	offset := int64(0)
	sz := d.SizeBytes
	for offset < sz {
		end := min(offset+chunkSize, sz)
		req := &bspb.WriteRequest{
			ResourceName: writeResourceName,
			WriteOffset:  offset,
			FinishWrite:  end == sz,
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
	if resp.CommittedSize != sz {
		t.Fatalf("CommittedSize = %d, want %d", resp.CommittedSize, sz)
	}
}
