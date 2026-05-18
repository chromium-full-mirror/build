// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/digest"
	"go.chromium.org/build/kajiya/execution/localexec"
)

const benchBufSize = 4 * 1024 * 1024

// BenchmarkExecuteE2E benchmarks the full gRPC Execute path with synthetic
// actions running in parallel (GOMAXPROCS goroutines). Use -cpuprofile to
// find bottlenecks:
//
//	go test -bench=BenchmarkExecuteE2E -cpuprofile=cpu.prof -benchtime=100x ./execution/
//	go tool pprof cpu.prof
func BenchmarkExecuteE2E(b *testing.B) {
	if _, err := exec.LookPath("nsjail"); err != nil {
		b.Skip("nsjail not found in PATH")
	}

	// Suppress INFO/WARN logs that interfere with benchmark output.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))

	for _, strategy := range []struct {
		name string
		s    localexec.SandboxStrategy
	}{
		{"overlay", localexec.OverlayFS},
		{"fuse", localexec.FuseFS},
	} {
		for _, numFiles := range []int{100, 1000, 5000} {
			name := fmt.Sprintf("%s/files=%d", strategy.name, numFiles)
			b.Run(name, func(b *testing.B) {
				ctx := b.Context()
				baseDir := b.TempDir()

				// Create CAS.
				cas, err := blobstore.New(ctx, filepath.Join(baseDir, "cas"), true)
				if err != nil {
					b.Fatal(err)
				}

				// Create executor.
				executor, err := localexec.New(filepath.Join(baseDir, "exec"), cas, strategy.s, false)
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { executor.Close() })

				// Start in-memory gRPC server with Execution service (no action cache).
				lis := bufconn.Listen(benchBufSize)
				srv := grpc.NewServer()
				if err := Register(srv, executor, nil, cas); err != nil {
					b.Fatal(err)
				}
				go func() { _ = srv.Serve(lis) }()
				b.Cleanup(func() { srv.Stop(); lis.Close() })

				// Create gRPC client.
				conn, err := grpc.NewClient("passthrough://bufnet",
					grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
						return lis.Dial()
					}),
					grpc.WithTransportCredentials(insecure.NewCredentials()),
				)
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { conn.Close() })
				client := repb.NewExecutionClient(conn)

				// Upload action protos to CAS: 3 nesting levels, 4 outputs of 100KB.
				actionDigest := putBenchProtos(b, cas, numFiles, 3, 4, 100<<10)

				b.ReportAllocs()
				b.ResetTimer()

				// Run GOMAXPROCS goroutines, each sending Execute RPCs in a loop.
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						executeOne(b, ctx, client, actionDigest)
					}
				})
			})
		}
	}
}

// executeOne sends a single Execute RPC and drains the response stream.
func executeOne(b *testing.B, ctx context.Context, client repb.ExecutionClient, actionDigest digest.Digest) {
	b.Helper()
	stream, err := client.Execute(ctx, &repb.ExecuteRequest{
		ActionDigest:    actionDigest.ToProto(),
		SkipCacheLookup: true,
	})
	if err != nil {
		b.Fatal(err)
	}
	for {
		op, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			b.Fatal("stream ended without a done operation")
		}
		if err != nil {
			b.Fatal(err)
		}
		if !op.Done {
			continue
		}
		resp := &repb.ExecuteResponse{}
		if err := op.GetResponse().UnmarshalTo(resp); err != nil {
			b.Fatal(err)
		}
		if resp.Result.ExitCode != 0 {
			b.Fatalf("action exited with code %d", resp.Result.ExitCode)
		}
		return
	}
}

// putBenchProtos uploads a complete REAPI action proto tree into the CAS and
// returns the action digest. The input tree has numInputFiles spread across
// directories nested to dirDepth levels. The command creates numOutputs files
// of outputSize bytes each.
func putBenchProtos(tb testing.TB, cas *blobstore.ContentAddressableStorage,
	numInputFiles, dirDepth, numOutputs, outputSize int) digest.Digest {
	tb.Helper()

	// Single 1KB source file reused for all inputs.
	content := bytes.Repeat([]byte("x"), 1024)
	fileDigest, err := cas.Put(content)
	if err != nil {
		tb.Fatal(err)
	}

	// putDir marshals a Directory proto, stores it in CAS, returns its digest.
	putDir := func(dir *repb.Directory) *repb.Digest {
		tb.Helper()
		b, err := proto.Marshal(dir)
		if err != nil {
			tb.Fatal(err)
		}
		d, err := cas.Put(b)
		if err != nil {
			tb.Fatal(err)
		}
		return d.ToProto()
	}

	// Build leaf directories with files (20 per dir).
	const filesPerDir = 20
	numDirs := max((numInputFiles+filesPerDir-1)/filesPerDir, 1)

	leafDirNodes := make([]*repb.DirectoryNode, numDirs)
	fileIdx := 0
	for d := range numDirs {
		dir := &repb.Directory{}
		n := min(filesPerDir, numInputFiles-fileIdx)
		for range n {
			dir.Files = append(dir.Files, &repb.FileNode{
				Name:   fmt.Sprintf("f%06d.h", fileIdx),
				Digest: fileDigest.ToProto(),
			})
			fileIdx++
		}
		leafDirNodes[d] = &repb.DirectoryNode{
			Name:   fmt.Sprintf("d%04d", d),
			Digest: putDir(dir),
		}
	}

	// Build nesting levels bottom-up.
	childNodes := leafDirNodes
	for level := dirDepth - 1; level >= 0; level-- {
		dir := &repb.Directory{Directories: childNodes}
		childNodes = []*repb.DirectoryNode{{
			Name:   fmt.Sprintf("n%d", level),
			Digest: putDir(dir),
		}}
	}

	// src/ directory containing the nesting tree (or leaf dirs if depth=0).
	srcDigest := putDir(&repb.Directory{Directories: childNodes})

	// out/ directory (empty; outputs declared via Command.OutputPaths).
	outDigest := putDir(&repb.Directory{})

	// Root directory.
	rootDigest := putDir(&repb.Directory{
		Directories: []*repb.DirectoryNode{
			{Name: "out", Digest: outDigest},
			{Name: "src", Digest: srcDigest},
		},
	})

	// Command proto.
	//
	// The command reads every input file (simulating a compiler reading headers)
	// and then creates output files. "cat src/**/* > /dev/null" forces reads
	// through the FUSE layer for every input, then dd creates outputs.
	outputPaths := make([]string, numOutputs)
	var cmdParts []string
	cmdParts = append(cmdParts, "find src -type f -exec cat {} + > /dev/null")
	for i := range numOutputs {
		outputPaths[i] = fmt.Sprintf("out/output%d.o", i)
		cmdParts = append(cmdParts, fmt.Sprintf(
			"dd if=/dev/zero of=out/output%d.o bs=%d count=1 2>/dev/null", i, outputSize))
	}
	cmd := &repb.Command{
		Arguments:   []string{"/bin/sh", "-c", strings.Join(cmdParts, " && ")},
		OutputPaths: outputPaths,
	}
	cmdBytes, err := proto.Marshal(cmd)
	if err != nil {
		tb.Fatal(err)
	}
	cmdDigest, err := cas.Put(cmdBytes)
	if err != nil {
		tb.Fatal(err)
	}

	// Action proto.
	action := &repb.Action{
		CommandDigest:   cmdDigest.ToProto(),
		InputRootDigest: rootDigest,
		DoNotCache:      true,
	}
	actionBytes, err := proto.Marshal(action)
	if err != nil {
		tb.Fatal(err)
	}
	actionDigest, err := cas.Put(actionBytes)
	if err != nil {
		tb.Fatal(err)
	}

	return actionDigest
}
