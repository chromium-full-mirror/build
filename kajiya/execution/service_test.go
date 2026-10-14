// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/model"
	"go.chromium.org/build/kajiya/server"
)

// mustFn returns the digest.Function for the given enum value, failing the
// test on unsupported values.
func mustFn(t testing.TB, v repb.DigestFunction_Value) digest.Function {
	t.Helper()
	fn, err := digest.Lookup(v)
	if err != nil {
		t.Fatal(err)
	}
	return fn
}

// TestDedupKey checks which in-flight Execute requests share a singleflight
// key: requests for the same action under the same digest function do, unless
// one of them is DoNotCache or only one of them sets skip_cache_lookup.
func TestDedupKey(t *testing.T) {
	const hash = "a9993e364706816aba3e25717850c26c9cd0d89d"
	d := digest.Digest{Hash: hash, SizeBytes: 3}
	sha1Fn := mustFn(t, repb.DigestFunction_SHA1)
	gitFn := mustFn(t, repb.DigestFunction_GITSHA1)
	op := uuid.New()

	// Same hash under different functions must not collide.
	if got, want := dedupKey(sha1Fn, d, op, false, false), "sha1/"+hash; got != want {
		t.Errorf("dedupKey(sha1) = %q, want %q", got, want)
	}
	if got, want := dedupKey(gitFn, d, op, false, false), "gitsha1/"+hash; got != want {
		t.Errorf("dedupKey(gitsha1) = %q, want %q", got, want)
	}
	if dedupKey(sha1Fn, d, op, false, false) == dedupKey(gitFn, d, op, false, false) {
		t.Errorf("dedupKey collides for sha1 and gitsha1 sharing hash %q", hash)
	}

	// DoNotCache uses the unique operation name so requests never merge.
	if got, want := dedupKey(sha1Fn, d, op, true, false), op.String(); got != want {
		t.Errorf("dedupKey(DoNotCache) = %q, want %q", got, want)
	}
	if got, want := dedupKey(sha1Fn, d, op, true, true), op.String(); got != want {
		t.Errorf("dedupKey(DoNotCache, skip_cache_lookup) = %q, want %q", got, want)
	}

	// skip_cache_lookup requests merge with each other but not with requests
	// that allow a cache lookup.
	other := uuid.New()
	if got, want := dedupKey(sha1Fn, d, op, false, true), dedupKey(sha1Fn, d, other, false, true); got != want {
		t.Errorf("dedupKey(skip_cache_lookup) = %q for one operation and %q for another, want equal", got, want)
	}
	if dedupKey(sha1Fn, d, op, false, true) == dedupKey(sha1Fn, d, other, false, false) {
		t.Errorf("dedupKey is %q with and without skip_cache_lookup, want different keys", dedupKey(sha1Fn, d, op, false, true))
	}
}

// TestExecutePassesSkipCacheLookup checks that the service hands each
// request's skip_cache_lookup to the executor in the Action.
func TestExecutePassesSkipCacheLookup(t *testing.T) {
	for _, tc := range []struct {
		name            string
		skipCacheLookup bool
	}{
		{name: "skip_cache_lookup", skipCacheLookup: true},
		{name: "cache_lookup", skipCacheLookup: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Start a service whose executor records the actions it gets.
			exec := &recordingExecutor{}
			client, cas := startTestService(t, exec)
			actionDigest := putTrivialAction(t, cas)

			// Execute the action and read the stream to its end.
			stream, err := client.Execute(t.Context(), &repb.ExecuteRequest{
				ActionDigest:    actionDigest.Proto(),
				SkipCacheLookup: tc.skipCacheLookup,
			})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			for {
				_, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("Recv: %v", err)
				}
			}

			// Check the Action of the single executor call.
			calls := exec.calls()
			if got, want := len(calls), 1; got != want {
				t.Fatalf("got %d executor calls, want %d", got, want)
			}
			if got, want := calls[0].SkipCacheLookup, tc.skipCacheLookup; got != want {
				t.Errorf("Action.SkipCacheLookup = %t, want %t", got, want)
			}
		})
	}
}

// TestExecuteSkipCacheLookupDoesNotMerge checks that a skip_cache_lookup
// request for an action that a request without the flag is executing gets an
// execution of its own, with SkipCacheLookup set, instead of joining the
// running one, whose executor may serve it a result from before the request.
func TestExecuteSkipCacheLookupDoesNotMerge(t *testing.T) {
	// Start a service whose executor blocks every execution until released.
	exec := &gatedExecutor{
		entered: make(chan *model.Action, 2),
		release: make(chan struct{}),
	}
	client, cas := startTestService(t, exec)
	release := sync.OnceFunc(func() { close(exec.release) })
	t.Cleanup(release)
	actionDigest := putTrivialAction(t, cas)

	// Start executing the action without skip_cache_lookup and wait until the
	// executor runs it.
	first, err := client.Execute(t.Context(), &repb.ExecuteRequest{
		ActionDigest: actionDigest.Proto(),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := exec.waitEntered(t).SkipCacheLookup, false; got != want {
		t.Errorf("first execution: SkipCacheLookup = %t, want %t", got, want)
	}

	// Request the same action with skip_cache_lookup while the first
	// execution is blocked. The request reaches the executor only if it did
	// not join that execution.
	second, err := client.Execute(t.Context(), &repb.ExecuteRequest{
		ActionDigest:    actionDigest.Proto(),
		SkipCacheLookup: true,
	})
	if err != nil {
		t.Fatalf("Execute(skip_cache_lookup): %v", err)
	}
	if got, want := exec.waitEntered(t).SkipCacheLookup, true; got != want {
		t.Errorf("second execution: SkipCacheLookup = %t, want %t", got, want)
	}

	// Let both executions finish and check that both requests complete.
	release()
	for _, stream := range []repb.Execution_ExecuteClient{first, second} {
		var last *longrunningpb.Operation
		for {
			op, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("Recv: %v", err)
			}
			last = op
		}
		if got, want := last.GetDone(), true; got != want {
			t.Errorf("last operation Done = %t, want %t", got, want)
		}
	}
}

// TestExecuteRespectsMaxParallelism checks that the service runs at most as
// many actions at once as the executor's MaxParallelism allows, and runs the
// queued ones once the running ones finish.
func TestExecuteRespectsMaxParallelism(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Start a service whose executor allows two executions at once and
		// blocks every execution until released.
		const numActions = 5
		exec := &gatedExecutor{
			entered: make(chan *model.Action, numActions),
			release: make(chan struct{}),
		}
		client, cas := startTestService(t, exec)
		release := sync.OnceFunc(func() { close(exec.release) })
		t.Cleanup(release)

		// Start distinct actions, which the service doesn't merge, and wait
		// until each one runs or waits for a slot.
		var streams []repb.Execution_ExecuteClient
		for i := range numActions {
			stream, err := client.Execute(t.Context(), &repb.ExecuteRequest{
				ActionDigest: putTrivialAction(t, cas, fmt.Sprint(i)).Proto(),
			})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			streams = append(streams, stream)
		}
		synctest.Wait()
		if got, want := len(exec.entered), 2; got != want {
			t.Fatalf("got %d running executor calls, want %d", got, want)
		}

		// Release the executions, and check that every request completes.
		release()
		for _, stream := range streams {
			var last *longrunningpb.Operation
			for {
				op, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("Recv: %v", err)
				}
				last = op
			}
			if got, want := last.GetDone(), true; got != want {
				t.Errorf("last operation Done = %t, want %t", got, want)
			}
		}
		if got, want := len(exec.entered), numActions; got != want {
			t.Errorf("got %d executor calls, want %d", got, want)
		}
	})
}

// testBufSize is the buffer size of the in-memory listener that test
// services serve on.
const testBufSize = 4 * 1024 * 1024

// startTestService starts an in-memory gRPC server hosting an Execution
// service backed by exec and its own CAS, without an action cache, and returns
// a client connected to it along with the CAS. The server stops when the test
// ends.
func startTestService(t *testing.T, exec ExecutorInterface) (repb.ExecutionClient, *blobstore.ContentAddressableStorage) {
	t.Helper()

	// Create the CAS.
	cas, err := blobstore.New(t.Context(), filepath.Join(t.TempDir(), "cas"))
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}

	// Serve the Execution service on an in-memory listener.
	lis := bufconn.Listen(testBufSize)
	srv := grpc.NewServer()
	if err := Register(srv, exec, nil, cas, server.Config{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Stop(); _ = lis.Close() })

	// Connect a client to it.
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return repb.NewExecutionClient(conn), cas
}

// putTrivialAction uploads the smallest Action that model.LoadAction accepts,
// a no-op command over an empty input root, and returns its digest. The
// command passes extraArgs to /bin/true, so different extraArgs give actions
// with different digests, which the service doesn't merge.
func putTrivialAction(t *testing.T, cas *blobstore.ContentAddressableStorage, extraArgs ...string) digest.Digest {
	t.Helper()
	put := func(m proto.Message) *repb.Digest {
		t.Helper()
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatalf("proto.Marshal: %v", err)
		}
		d, err := cas.Put(digest.SHA256, b)
		if err != nil {
			t.Fatalf("cas.Put: %v", err)
		}
		return d.Proto()
	}
	rootDigest := put(&repb.Directory{})
	cmdDigest := put(&repb.Command{Arguments: append([]string{"/bin/true"}, extraArgs...)})
	d, err := digest.SHA256.FromProto(put(&repb.Action{CommandDigest: cmdDigest, InputRootDigest: rootDigest}))
	if err != nil {
		t.Fatalf("action digest: %v", err)
	}
	return d
}

// recordingExecutor is an ExecutorInterface that records the Action of every
// Execute call and returns an empty ActionResult. It is safe for concurrent
// use.
type recordingExecutor struct {
	mu      sync.Mutex
	actions []*model.Action
}

// Execute records action and returns an empty ActionResult.
func (r *recordingExecutor) Execute(action *model.Action) (*repb.ActionResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = append(r.actions, action)
	return &repb.ActionResult{}, nil
}

// MaxParallelism returns 0, which means no limit.
func (r *recordingExecutor) MaxParallelism() int {
	return 0
}

// calls returns the Action of every Execute call so far, in call order.
func (r *recordingExecutor) calls() []*model.Action {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.actions)
}

// gatedExecutor is an ExecutorInterface whose Execute calls report their
// Action on entered and then block until release is closed. entered must have
// room for every call the test makes.
type gatedExecutor struct {
	entered chan *model.Action
	release chan struct{}
}

// MaxParallelism returns 2, so that two executions can block at once.
func (g *gatedExecutor) MaxParallelism() int {
	return 2
}

// Execute sends action to entered, waits for release to close, and returns an
// empty ActionResult.
func (g *gatedExecutor) Execute(action *model.Action) (*repb.ActionResult, error) {
	g.entered <- action
	<-g.release
	return &repb.ActionResult{}, nil
}

// waitEntered returns the Action of the next Execute call. It fails the test
// if no call starts within 10 seconds, far longer than a request that nothing
// blocks takes to reach the executor.
func (g *gatedExecutor) waitEntered(t *testing.T) *model.Action {
	t.Helper()
	select {
	case action := <-g.entered:
		return action
	case <-time.After(10 * time.Second):
		t.Fatal("no Execute call started within 10 seconds")
		return nil
	}
}
