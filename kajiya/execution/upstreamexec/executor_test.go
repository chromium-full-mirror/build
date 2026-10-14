// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package upstreamexec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	rpcstatuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/actioncache"
	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/capabilities"
	"go.chromium.org/build/kajiya/execution"
	"go.chromium.org/build/kajiya/execution/model"
	"go.chromium.org/build/kajiya/execution/upstreamexec"
	"go.chromium.org/build/kajiya/server"
)

// bufSize is the bufconn buffer size and the gRPC server's receive limit in
// these tests. It matches gRPC's default receive limit, which a real upstream
// would likely keep.
const bufSize = 4 * 1024 * 1024

// fnsForTest parses digest function names into a Function slice.
func fnsForTest(t *testing.T, names ...string) []digest.Function {
	t.Helper()
	fns := make([]digest.Function, 0, len(names))
	for _, n := range names {
		fn, err := digest.ParseFunction(n)
		if err != nil {
			t.Fatalf("ParseFunction(%q): %v", n, err)
		}
		fns = append(fns, fn)
	}
	return fns
}

// fillBytes returns size bytes of b.
func fillBytes(b byte, size int) []byte {
	return []byte(strings.Repeat(string(b), size))
}

// loadHelloAction stages the Action protos of putHelloAction and returns a
// model.Action ready to feed to executor.Execute.
func loadHelloAction(t *testing.T, cas *blobstore.ContentAddressableStorage) *model.Action {
	t.Helper()
	return loadHelloActionFn(t, cas, digest.SHA256)
}

// loadHelloActionFn is loadHelloAction under an arbitrary digest function.
func loadHelloActionFn(t *testing.T, cas *blobstore.ContentAddressableStorage, fn digest.Function) *model.Action {
	t.Helper()
	return loadAction(t, cas, fn, putHelloAction(t, cas, fn))
}

// loadAction loads the action with digest ad from cas.
func loadAction(t *testing.T, cas *blobstore.ContentAddressableStorage, fn digest.Function, ad digest.Digest) *model.Action {
	t.Helper()
	a, err := model.LoadAction(fn, ad, cas)
	if err != nil {
		t.Fatalf("load action: %v", err)
	}
	return a
}

// stagingFiles returns the names of the upload staging files of the CAS in
// casDir.
func stagingFiles(t *testing.T, casDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(casDir, "tmp"))
	if err != nil {
		t.Fatalf("read staging directory: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestExecuteRejectsUnnegotiatedFunction verifies that the proxy fails an
// action whose digest function is outside the negotiated set up front with
// InvalidArgument instead of deep inside the upload phase.
func TestExecuteRejectsUnnegotiatedFunction(t *testing.T) {
	// Start a SHA-256 proxy and give it an action under BLAKE3.
	fake := newFakeUpstream(t, []byte("hello"))
	executor, cas := startProxy(t, fake)
	a := loadHelloAction(t, cas)
	a.Fn = fnsForTest(t, "blake3")[0]

	// Execute it and check that it fails before reaching the upstream.
	_, err := executor.Execute(a)
	if got, want := status.Code(err), codes.InvalidArgument; got != want {
		t.Fatalf("status code = %s, want %s (err: %v)", got, want, err)
	}
	if got, want := fake.findMissingCalls.Load()+fake.executeCalls.Load(), int64(0); got != want {
		t.Errorf("upstream saw %d RPCs, want %d (rejection must happen before any upload)", got, want)
	}
}

// TestNewDigestFunctionNegotiation verifies that New negotiates the locally
// configured digest functions that the upstream supports for both cache and
// execution, and fails when there are none. Absent upstream capabilities
// conservatively count as SHA-256 only.
func TestNewDigestFunctionNegotiation(t *testing.T) {
	tests := []struct {
		name              string
		localFns          []string
		upstreamFns       []repb.DigestFunction_Value
		upstreamExecFns   []repb.DigestFunction_Value
		upstreamExecFn    repb.DigestFunction_Value
		upstreamExecOff   bool
		capsUnimplemented bool
		wantFns           []string
		wantErr           []string
	}{
		{
			name:        "sha256_only",
			localFns:    []string{"sha256"},
			upstreamFns: []repb.DigestFunction_Value{repb.DigestFunction_SHA256},
			wantFns:     []string{"sha256"},
		},
		{
			name:        "extra_local_functions_narrowed",
			localFns:    []string{"sha256", "blake3"},
			upstreamFns: []repb.DigestFunction_Value{repb.DigestFunction_SHA256},
			wantFns:     []string{"sha256"},
		},
		{
			name:            "full_overlap",
			localFns:        []string{"sha256", "blake3"},
			upstreamFns:     []repb.DigestFunction_Value{repb.DigestFunction_BLAKE3, repb.DigestFunction_SHA256},
			upstreamExecFns: []repb.DigestFunction_Value{repb.DigestFunction_SHA256, repb.DigestFunction_BLAKE3},
			wantFns:         []string{"sha256", "blake3"},
		},
		{
			name:            "blake3_only_upstream",
			localFns:        []string{"blake3"},
			upstreamFns:     []repb.DigestFunction_Value{repb.DigestFunction_BLAKE3},
			upstreamExecFns: []repb.DigestFunction_Value{repb.DigestFunction_BLAKE3},
			wantFns:         []string{"blake3"},
		},
		{
			name:            "exec_set_narrows_cache_set",
			localFns:        []string{"sha256", "blake3"},
			upstreamFns:     []repb.DigestFunction_Value{repb.DigestFunction_SHA256, repb.DigestFunction_BLAKE3},
			upstreamExecFns: []repb.DigestFunction_Value{repb.DigestFunction_SHA256},
			wantFns:         []string{"sha256"},
		},
		{
			// The deprecated singular field counts when the repeated set
			// is absent.
			name:           "singular_exec_function_only",
			localFns:       []string{"sha256", "blake3"},
			upstreamFns:    []repb.DigestFunction_Value{repb.DigestFunction_SHA256, repb.DigestFunction_BLAKE3},
			upstreamExecFn: repb.DigestFunction_BLAKE3,
			wantFns:        []string{"blake3"},
		},
		{
			// The repeated set is authoritative: an inconsistent singular
			// field must not widen it.
			name:            "plural_overrides_singular",
			localFns:        []string{"sha256", "blake3"},
			upstreamFns:     []repb.DigestFunction_Value{repb.DigestFunction_SHA256, repb.DigestFunction_BLAKE3},
			upstreamExecFns: []repb.DigestFunction_Value{repb.DigestFunction_BLAKE3},
			upstreamExecFn:  repb.DigestFunction_SHA256,
			wantFns:         []string{"blake3"},
		},
		{
			name:            "exec_disabled_rejected",
			localFns:        []string{"sha256"},
			upstreamFns:     []repb.DigestFunction_Value{repb.DigestFunction_SHA256},
			upstreamExecOff: true,
			wantErr:         []string{"exec_enabled"},
		},
		{
			name:        "no_common_function_rejected",
			localFns:    []string{"blake3"},
			upstreamFns: []repb.DigestFunction_Value{repb.DigestFunction_SHA256},
			wantErr:     []string{"blake3", "SHA256"},
		},
		{
			name:            "cache_exec_disjoint_rejected",
			localFns:        []string{"sha256", "blake3"},
			upstreamFns:     []repb.DigestFunction_Value{repb.DigestFunction_SHA256},
			upstreamExecFns: []repb.DigestFunction_Value{repb.DigestFunction_BLAKE3},
			wantErr:         []string{"cache", "execution"},
		},
		{
			name:              "unimplemented_capabilities_sha256_fallback",
			localFns:          []string{"sha256", "blake3"},
			capsUnimplemented: true,
			wantFns:           []string{"sha256"},
		},
		{
			name:              "unimplemented_capabilities_no_sha256_rejected",
			localFns:          []string{"blake3"},
			capsUnimplemented: true,
			wantErr:           []string{"blake3", "SHA256"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Start a fake upstream with the case's capabilities, and a
			// local CAS.
			fake := newFakeUpstream(t, []byte("hello"))
			fake.capsUnimplemented = tc.capsUnimplemented
			fake.capsDigestFunctions = tc.upstreamFns
			fake.capsExecDigestFunctions = tc.upstreamExecFns
			fake.capsExecDigestFunction = tc.upstreamExecFn
			fake.capsExecDisabled = tc.upstreamExecOff
			conn := dialFakeUpstream(t, fake)
			cas := newCAS(t, filepath.Join(t.TempDir(), "cas"), fnsForTest(t, tc.localFns...)...)

			// Create the executor and check its error or the negotiated
			// set.
			executor, err := upstreamexec.New(t.Context(), conn, cas, fakeInstance, fnsForTest(t, tc.localFns...))
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatal("New succeeded, want error")
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v, want success", err)
			}
			if got, want := executor.DigestFunctions(), fnsForTest(t, tc.wantFns...); !slices.Equal(got, want) {
				t.Errorf("DigestFunctions() = %v, want %v", got, want)
			}
		})
	}
}

// TestExecuteSkipsInputsKnownPresent verifies that the second Execute for the
// same Action does not call FindMissingBlobs or BatchUpdateBlobs upstream,
// because the presence cache populated by the first call covers every input.
func TestExecuteSkipsInputsKnownPresent(t *testing.T) {
	// Start a proxy in front of a fake upstream.
	fake := newFakeUpstream(t, []byte("hello\n"))
	executor, cas := startProxy(t, fake)
	action := loadHelloAction(t, cas)

	// Execute once and check that the proxy checked and uploaded the inputs,
	// which fit in one request each.
	if _, err := executor.Execute(action); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	fm1 := fake.findMissingCalls.Load()
	bu1 := fake.batchUpdateCalls.Load()
	if got, want := fm1, int64(1); got != want {
		t.Fatalf("FindMissingBlobs calls after first Execute = %d, want %d", got, want)
	}
	if got, want := bu1, int64(1); got != want {
		t.Fatalf("BatchUpdateBlobs calls after first Execute = %d, want %d", got, want)
	}

	// Execute again and check that only the Execute call reached the
	// upstream.
	if _, err := executor.Execute(action); err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if got, want := fake.findMissingCalls.Load(), fm1; got != want {
		t.Errorf("FindMissingBlobs calls after second Execute = %d, want %d", got, want)
	}
	if got, want := fake.batchUpdateCalls.Load(), bu1; got != want {
		t.Errorf("BatchUpdateBlobs calls after second Execute = %d, want %d", got, want)
	}
	if got, want := fake.executeCalls.Load(), int64(2); got != want {
		t.Errorf("Execute call count = %d, want %d", got, want)
	}
}

// TestExecuteChecksEachInputOnce verifies that the proxy lists an input blob
// once in FindMissingBlobs although the input tree references it twice. The
// fake upstream rejects a request that lists a digest twice.
func TestExecuteChecksEachInputOnce(t *testing.T) {
	// Stage an action with two input files of the same content.
	fake := newFakeUpstream(t, []byte("hello\n"))
	executor, cas := startProxy(t, fake)
	ad := putActionWithInputs(t, cas, digest.SHA256, map[string][]byte{
		"a.txt": []byte("same\n"),
		"b.txt": []byte("same\n"),
	})

	// Execute it, which fails if the proxy lists the content twice.
	if _, err := executor.Execute(loadAction(t, cas, digest.SHA256, ad)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// TestExecuteSharesInFlightUploads verifies that two concurrent Execute calls
// over the same Action share their FindMissingBlobs and BatchUpdateBlobs:
// only one call issues those upstream RPCs while the other waits for its
// uploads.
func TestExecuteSharesInFlightUploads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Start a proxy in front of a fake upstream whose BatchUpdateBlobs
		// blocks until released.
		fake := newFakeUpstream(t, []byte("hello\n"))
		fake.batchUpdateGate = make(chan struct{})
		executor, cas := startProxy(t, fake)
		action := loadHelloAction(t, cas)

		// Start A and wait until it blocks in BatchUpdateBlobs. By then A
		// holds the claim on every input.
		type result struct {
			ar  *repb.ActionResult
			err error
		}
		chA := make(chan result, 1)
		chB := make(chan result, 1)
		run := func(ch chan<- result) {
			ar, err := executor.Execute(action)
			ch <- result{ar, err}
		}
		go run(chA)
		synctest.Wait()

		// Start B, check that it waits for A's upload, and release that.
		go run(chB)
		synctest.Wait()
		select {
		case r := <-chB:
			t.Fatalf("B finished before A's upload did: %v", r.err)
		default:
		}
		close(fake.batchUpdateGate)

		// Wait for both and check that only A checked and uploaded the inputs.
		rA, rB := <-chA, <-chB
		if rA.err != nil || rB.err != nil {
			t.Fatalf("Execute: A: %v, B: %v", rA.err, rB.err)
		}
		if got, want := fake.findMissingCalls.Load(), int64(1); got != want {
			t.Errorf("FindMissingBlobs calls = %d, want %d (B must wait for A's uploads)", got, want)
		}
		if got, want := fake.batchUpdateCalls.Load(), int64(1); got != want {
			t.Errorf("BatchUpdateBlobs calls = %d, want %d (B must not re-upload)", got, want)
		}
		if got, want := fake.executeCalls.Load(), int64(2); got != want {
			t.Errorf("Execute call count = %d, want %d (each call still issues its own Execute RPC)", got, want)
		}
		if got, want := rB.ar.OutputFiles[0].Digest, rA.ar.OutputFiles[0].Digest; !proto.Equal(got, want) {
			t.Errorf("output digest of B = %v, want A's %v", got, want)
		}
	})
}

// TestExecuteRecoversMissingBlobs verifies how Execute handles an upstream
// that reports a blob missing in the ExecuteResponse although the presence
// cache had memoized it, as after RBE evicts the blob.
func TestExecuteRecoversMissingBlobs(t *testing.T) {
	tests := []struct {
		name string

		// evict removes the reported blob from the upstream.
		evict bool

		// fabricated reports a blob that the local CAS lacks too, instead
		// of the action's Command.
		fabricated bool

		// missingOn lists the Execute calls that report the blob missing.
		// Call 1 primes the presence cache.
		missingOn []int64

		// wantCode is the code of Execute's error, or OK for success.
		wantCode codes.Code

		// wantExecuteCalls and wantBatchUpdates are the Execute and
		// BatchUpdateBlobs calls after the first one.
		wantExecuteCalls int64
		wantBatchUpdates int64
	}{
		{
			// Recovery checks every input, uploads the evicted one, and
			// retries.
			name:             "evicted_blob",
			evict:            true,
			missingOn:        []int64{2},
			wantCode:         codes.OK,
			wantExecuteCalls: 2,
			wantBatchUpdates: 1,
		},
		{
			// Neither a retry nor passing the report on would help, and a
			// plain error isn't FAILED_PRECONDITION, which would make the
			// client upload again.
			name:             "missing_locally_too",
			fabricated:       true,
			missingOn:        []int64{2},
			wantCode:         codes.Unknown,
			wantExecuteCalls: 1,
		},
		{
			// The upstream has just reported every input present, so
			// passing FAILED_PRECONDITION on would make the client re-upload
			// blobs that the local CAS already has.
			name:             "reported_again_after_retry",
			missingOn:        []int64{2, 3},
			wantCode:         codes.Internal,
			wantExecuteCalls: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Execute once, which primes the presence cache for every input.
			fake := newFakeUpstream(t, []byte("hello\n"))
			executor, cas := startProxy(t, fake)
			action := loadHelloAction(t, cas)
			if _, err := executor.Execute(action); err != nil {
				t.Fatalf("first Execute: %v", err)
			}
			exAfterFirst := fake.executeCalls.Load()
			buAfterFirst := fake.batchUpdateCalls.Load()

			// Make the upstream report a blob missing.
			reported := action.CommandDigest
			if tc.fabricated {
				reported = digest.SHA256.FromBytes([]byte("not an input"))
			}
			if tc.evict {
				fake.mu.Lock()
				delete(fake.blobs, reported)
				fake.mu.Unlock()
			}
			fake.executeOps = missingOnCalls(t, digest.SHA256, []digest.Digest{reported}, tc.missingOn...)

			// Execute again and check the outcome and the upstream RPCs.
			_, err := executor.Execute(action)
			if got, want := status.Code(err), tc.wantCode; got != want {
				t.Errorf("status code = %s, want %s (err: %v)", got, want, err)
			}
			if got, want := fake.executeCalls.Load()-exAfterFirst, tc.wantExecuteCalls; got != want {
				t.Errorf("Execute calls = %d, want %d", got, want)
			}
			if got, want := fake.batchUpdateCalls.Load()-buAfterFirst, tc.wantBatchUpdates; got != want {
				t.Errorf("BatchUpdateBlobs calls = %d, want %d", got, want)
			}
			if tc.evict {
				fake.mu.Lock()
				_, ok := fake.blobs[reported]
				fake.mu.Unlock()
				if got, want := ok, true; got != want {
					t.Errorf("evicted blob is upstream again = %t, want %t", got, want)
				}
			}
		})
	}
}

// TestExecuteRecoveryWaitsForInFlightUpload verifies that recovering from an
// upstream missing-blobs report waits for an upload of the same blob that
// another Execute call's recovery has in flight, instead of uploading the
// blob a second time.
func TestExecuteRecoveryWaitsForInFlightUpload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Execute once, which primes the presence cache for every input.
		fake := newFakeUpstream(t, []byte("hello\n"))
		executor, cas := startProxy(t, fake)
		action := loadHelloAction(t, cas)
		if _, err := executor.Execute(action); err != nil {
			t.Fatalf("first Execute: %v", err)
		}
		buAfterFirst := fake.batchUpdateCalls.Load()

		// Evict the Command upstream, and make Execute calls 2 and 3 report
		// it missing. Call 2 answers only once released, and
		// BatchUpdateBlobs blocks until released.
		fake.mu.Lock()
		delete(fake.blobs, action.CommandDigest)
		fake.mu.Unlock()
		missing := missingOnCalls(t, digest.SHA256, []digest.Digest{action.CommandDigest}, 2, 3)
		releaseB := make(chan struct{})
		fake.executeOps = func(call int64, req *repb.ExecuteRequest) []*longrunningpb.Operation {
			if call == 2 {
				<-releaseB
			}
			return missing(call, req)
		}
		fake.batchUpdateGate = make(chan struct{})

		// Start B, whose Execute call waits upstream. Then start A, whose
		// recovery claims the Command and blocks uploading it.
		errB := make(chan error, 1)
		errA := make(chan error, 1)
		go func() { _, err := executor.Execute(action); errB <- err }()
		synctest.Wait()
		go func() { _, err := executor.Execute(action); errA <- err }()
		synctest.Wait()

		// Let B's recovery start while A's upload is in flight, then let the
		// upload finish.
		close(releaseB)
		synctest.Wait()
		close(fake.batchUpdateGate)

		// Check that both succeed and that only A uploaded the Command.
		if err := <-errA; err != nil {
			t.Errorf("Execute A: %v", err)
		}
		if err := <-errB; err != nil {
			t.Errorf("Execute B: %v", err)
		}
		if got, want := fake.batchUpdateCalls.Load()-buAfterFirst, int64(1); got != want {
			t.Errorf("BatchUpdateBlobs calls = %d, want %d", got, want)
		}
	})
}

// TestExecuteParsesMissingBlobSubjects verifies which PreconditionFailure
// subjects of an upstream's missing-blobs report Execute recovers from. It
// passes on a report that names no blob it can parse as FAILED_PRECONDITION.
func TestExecuteParsesMissingBlobSubjects(t *testing.T) {
	tests := []struct {
		name string
		fn   string

		// violationType and subject describe the reported violation. The
		// subject's %[1]s is the Command's hash and %[2]d its size.
		violationType string
		subject       string

		// wantRecovered tells whether Execute recovers and retries.
		wantRecovered bool
	}{
		{name: "sha256_without_segment", fn: "sha256", violationType: "MISSING", subject: "blobs/%[1]s/%[2]d", wantRecovered: true},
		// Buildbarn and Buildfarm send the segment of a function that has
		// one, while BuildBuddy and NativeLink leave it out.
		{name: "blake3_with_segment", fn: "blake3", violationType: "MISSING", subject: "blobs/blake3/%[1]s/%[2]d", wantRecovered: true},
		{name: "blake3_without_segment", fn: "blake3", violationType: "MISSING", subject: "blobs/%[1]s/%[2]d", wantRecovered: true},
		{name: "wrong_segment", fn: "blake3", violationType: "MISSING", subject: "blobs/sha256/%[1]s/%[2]d"},
		{name: "wrong_prefix", fn: "sha256", violationType: "MISSING", subject: "bobs/%[1]s/%[2]d"},
		{name: "bad_size", fn: "sha256", violationType: "MISSING", subject: "blobs/%[1]s/five"},
		{name: "bad_hash", fn: "sha256", violationType: "MISSING", subject: "blobs/zz/%[2]d"},
		{name: "empty", fn: "sha256", violationType: "MISSING", subject: ""},
		{name: "not_missing", fn: "sha256", violationType: "OTHER", subject: "blobs/%[1]s/%[2]d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Start a proxy for the case's function.
			fn := fnsForTest(t, tc.fn)[0]
			fake := newFakeUpstream(t, []byte("hello\n"))
			fake.capsDigestFunctions = []repb.DigestFunction_Value{fn.Value()}
			fake.capsExecDigestFunctions = []repb.DigestFunction_Value{fn.Value()}
			cas := newCAS(t, filepath.Join(t.TempDir(), "cas"), fn)
			executor := newProxy(t, fake, cas, fn)
			action := loadHelloActionFn(t, cas, fn)

			// Make the first Execute call report the Command with the case's
			// violation, and later ones succeed.
			subject := tc.subject
			if strings.Contains(subject, "%") {
				subject = fmt.Sprintf(subject, action.CommandDigest.Hash, action.CommandDigest.SizeBytes)
			}
			report := []*longrunningpb.Operation{doneOperation(t, &repb.ExecuteResponse{
				Status: preconditionStatus(t, tc.violationType, subject),
			})}
			success := []*longrunningpb.Operation{doneOperation(t, &repb.ExecuteResponse{
				Result: &repb.ActionResult{},
			})}
			fake.executeOps = func(call int64, _ *repb.ExecuteRequest) []*longrunningpb.Operation {
				if call == 1 {
					return report
				}
				return success
			}

			// Execute and check whether it retried.
			_, err := executor.Execute(action)
			wantCode, wantCalls := codes.FailedPrecondition, int64(1)
			if tc.wantRecovered {
				wantCode, wantCalls = codes.OK, 2
			}
			if got, want := status.Code(err), wantCode; got != want {
				t.Errorf("status code = %s, want %s (err: %v)", got, want, err)
			}
			if got, want := fake.executeCalls.Load(), wantCalls; got != want {
				t.Errorf("Execute calls = %d, want %d", got, want)
			}
		})
	}
}

// TestExecuteReportsUpstreamResponse verifies how Execute maps each shape of
// the upstream's final operation to a result or an error, and that an error
// keeps the upstream's code.
func TestExecuteReportsUpstreamResponse(t *testing.T) {
	tests := []struct {
		name string
		op   *longrunningpb.Operation

		// wantCode is the code of Execute's error, or OK for success.
		wantCode codes.Code

		// wantExitCode is the result's exit code on success.
		wantExitCode int32
	}{
		{
			// REAPI forbids Operation.error, but an upstream may set it.
			name: "operation_error",
			op: &longrunningpb.Operation{
				Done: true,
				Result: &longrunningpb.Operation_Error{Error: &rpcstatuspb.Status{
					Code:    int32(codes.Unavailable),
					Message: "backend down",
				}},
			},
			wantCode: codes.Unavailable,
		},
		{
			name:     "no_response",
			op:       &longrunningpb.Operation{Done: true},
			wantCode: codes.Unknown,
		},
		{
			name: "failed_status",
			op: doneOperation(t, &repb.ExecuteResponse{
				Status: &rpcstatuspb.Status{Code: int32(codes.Internal), Message: "worker crashed"},
			}),
			wantCode: codes.Internal,
		},
		{
			name:     "no_result",
			op:       doneOperation(t, &repb.ExecuteResponse{}),
			wantCode: codes.Unknown,
		},
		{
			name: "nonzero_exit_is_success",
			op: doneOperation(t, &repb.ExecuteResponse{
				Result: &repb.ActionResult{ExitCode: 3},
			}),
			wantCode:     codes.OK,
			wantExitCode: 3,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Make the upstream answer with the case's operation.
			fake := newFakeUpstream(t, []byte("hello\n"))
			fake.executeOps = func(int64, *repb.ExecuteRequest) []*longrunningpb.Operation {
				return []*longrunningpb.Operation{tc.op}
			}
			executor, cas := startProxy(t, fake)

			// Execute and check the outcome.
			ar, err := executor.Execute(loadHelloAction(t, cas))
			if got, want := status.Code(err), tc.wantCode; got != want {
				t.Fatalf("status code = %s, want %s (err: %v)", got, want, err)
			}
			if got, want := ar.GetExitCode(), tc.wantExitCode; got != want {
				t.Errorf("exit code = %d, want %d", got, want)
			}
		})
	}
}

// TestExecuteEndsStreamCleanly verifies that the proxy reads the upstream's
// Execute stream to its end, so that the call ends with OK instead of being
// canceled once the result is in hand.
func TestExecuteEndsStreamCleanly(t *testing.T) {
	// Execute an action through a proxy in front of a fake upstream.
	fake := newFakeUpstream(t, []byte("hello\n"))
	executor, cas := startProxy(t, fake)
	if _, err := executor.Execute(loadHelloAction(t, cas)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Check how the upstream call ended.
	if got, want := fake.stats.ends(executeMethod), []codes.Code{codes.OK}; !slices.Equal(got, want) {
		t.Errorf("Execute calls ended with %v, want %v", got, want)
	}
}

// TestExecuteCancelsStreamLeftOpen verifies that the proxy returns the result
// of an upstream that keeps the Execute stream open after the Done operation,
// and cancels the stream after a while.
func TestExecuteCancelsStreamLeftOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Execute an action through a proxy in front of a fake upstream that
		// never ends the stream.
		fake := newFakeUpstream(t, []byte("hello\n"))
		fake.holdExecuteStream = true
		executor, cas := startProxy(t, fake)
		ar, err := executor.Execute(loadHelloAction(t, cas))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}

		// Check the result and how the upstream call ended.
		if got, want := len(ar.OutputFiles), 1; got != want {
			t.Errorf("got %d output files, want %d", got, want)
		}
		if got, want := fake.stats.ends(executeMethod), []codes.Code{codes.Canceled}; !slices.Equal(got, want) {
			t.Errorf("Execute calls ended with %v, want %v", got, want)
		}
	})
}

// TestExecuteForwardsSkipCacheLookup verifies that the Action's
// SkipCacheLookup is threaded into the upstream ExecuteRequest. Otherwise a
// caller asking to bypass the cache (e.g. to reproduce a poisoned result)
// would still get a stale upstream cache hit.
func TestExecuteForwardsSkipCacheLookup(t *testing.T) {
	tests := []struct {
		name            string
		skipCacheLookup bool
	}{
		{name: "cache_lookup", skipCacheLookup: false},
		{name: "skip_cache_lookup", skipCacheLookup: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Execute the action with the case's SkipCacheLookup.
			fake := newFakeUpstream(t, []byte("hello\n"))
			executor, cas := startProxy(t, fake)
			action := loadHelloAction(t, cas)
			action.SkipCacheLookup = tc.skipCacheLookup
			if _, err := executor.Execute(action); err != nil {
				t.Fatalf("Execute: %v", err)
			}

			// Check what the upstream saw.
			fake.mu.Lock()
			got := fake.lastExecuteSkipCacheLookup
			fake.mu.Unlock()
			if want := tc.skipCacheLookup; got != want {
				t.Errorf("upstream saw SkipCacheLookup = %t, want %t", got, want)
			}
		})
	}
}

// TestExecuteFillsBothOutputDirectoryForms verifies that when the upstream
// returns an OutputDirectory with only RootDirectoryDigest set (and no
// TreeDigest), the proxy still pulls the root, nested directories, and every
// referenced file into the local CAS, and populates TreeDigest on the
// result so REAPI clients reading the cached entry can use either form.
func TestExecuteFillsBothOutputDirectoryForms(t *testing.T) {
	// Start a proxy in front of a fake upstream.
	fake := newFakeUpstream(t, []byte("hello\n"))
	executor, cas := startProxy(t, fake)
	action := loadHelloAction(t, cas)

	// Build a small directory tree in the fake's CAS:
	//   root/
	//     leaf.txt   (content "leaf")
	leafBytes := []byte("leaf")
	leafDigest := digest.SHA256.FromBytes(leafBytes)
	rootDir := &repb.Directory{
		Files: []*repb.FileNode{
			{Name: "leaf.txt", Digest: leafDigest.Proto()},
		},
	}
	rootDirBytes, err := proto.Marshal(rootDir)
	if err != nil {
		t.Fatalf("marshal root dir: %v", err)
	}
	rootDirDigest := digest.SHA256.FromBytes(rootDirBytes)

	// Store the tree upstream and make it the action's output directory,
	// with only RootDirectoryDigest.
	fake.mu.Lock()
	fake.blobs[leafDigest] = leafBytes
	fake.blobs[rootDirDigest] = rootDirBytes
	fake.mu.Unlock()
	fake.executeOps = respondWith(t, &repb.ExecuteResponse{Result: &repb.ActionResult{
		OutputDirectories: []*repb.OutputDirectory{
			{Path: "out", RootDirectoryDigest: rootDirDigest.Proto()},
		},
	}})

	// Execute the action through the proxy.
	ar, err := executor.Execute(action)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := len(ar.OutputDirectories), 1; got != want {
		t.Fatalf("OutputDirectories len = %d, want %d", got, want)
	}
	od := ar.OutputDirectories[0]

	// Both fields should be populated on the way out so callers using either
	// the Tree-blob form or the per-Directory form can resolve the result.
	rd, err := digest.SHA256.FromProto(od.RootDirectoryDigest)
	if err != nil {
		t.Fatalf("RootDirectoryDigest: %v", err)
	}
	td, err := digest.SHA256.FromProto(od.TreeDigest)
	if err != nil {
		t.Fatalf("TreeDigest (proxy should rebuild it from per-Directory blobs): %v", err)
	}

	// All referenced blobs must now live in the local CAS.
	for name, d := range map[string]digest.Digest{
		"RootDirectory blob": rd,
		"leaf file blob":     leafDigest,
		"rebuilt Tree blob":  td,
	} {
		if !cas.Has(digest.SHA256, d) {
			t.Errorf("local CAS missing %s %s", name, d)
		}
	}
}

// TestExecuteBlake3EndToEnd runs a BLAKE3 action through the proxy against a
// BLAKE3-capable fake upstream. It verifies that the negotiated function is
// carried on every upstream request and that the output blob lands in the
// local CAS under BLAKE3, without SHA-256 being involved anywhere. The input
// and the output are large enough to go through ByteStream, whose resource
// names carry BLAKE3's {digest_function} segment.
func TestExecuteBlake3EndToEnd(t *testing.T) {
	// Start a fake upstream that supports only BLAKE3 and holds the output
	// blob under BLAKE3.
	blake3 := fnsForTest(t, "blake3")[0]
	outputBytes := fillBytes('o', 3*1024*1024)
	fake := newFakeUpstream(t, outputBytes)
	fake.capsDigestFunctions = []repb.DigestFunction_Value{repb.DigestFunction_BLAKE3}
	fake.capsExecDigestFunctions = []repb.DigestFunction_Value{repb.DigestFunction_BLAKE3}
	od := blake3.FromBytes(outputBytes)
	stdout := []byte("hello\n")
	sd := blake3.FromBytes(stdout)
	fake.blobs = map[digest.Digest][]byte{od: outputBytes, sd: stdout}
	fake.outputDigest = od.Proto()
	fake.executeOps = respondWith(t, &repb.ExecuteResponse{Result: &repb.ActionResult{
		OutputFiles:  []*repb.OutputFile{{Path: "out/result.txt", Digest: od.Proto()}},
		StdoutDigest: sd.Proto(),
	}})

	// Create a proxy with a BLAKE3-only local CAS and check that it
	// negotiates BLAKE3.
	cas := newCAS(t, filepath.Join(t.TempDir(), "cas"), blake3)
	executor := newProxy(t, fake, cas, blake3)
	if got, want := executor.DigestFunctions(), []digest.Function{blake3}; !slices.Equal(got, want) {
		t.Fatalf("DigestFunctions() = %v, want %v", got, want)
	}

	// Run a BLAKE3 action and check that its output lands in the local CAS
	// under BLAKE3.
	a := loadAction(t, cas, blake3, putActionWithInputs(t, cas, blake3, map[string][]byte{
		"large": fillBytes('i', 3*1024*1024),
	}))
	ar, err := executor.Execute(a)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := ar.ExitCode, int32(0); got != want {
		t.Fatalf("exit code = %d, want %d", got, want)
	}
	gotOut, err := cas.Get(blake3, od)
	if err != nil {
		t.Fatalf("output blob not in local CAS under blake3: %v", err)
	}
	if got, want := string(gotOut), string(outputBytes); got != want {
		t.Errorf("output content differs: got %d bytes, want %d", len(got), len(want))
	}

	// Check that every upstream request carried BLAKE3, and that the large
	// blobs went through ByteStream.
	if got, want := fake.bsWriteCalls.Load(), int64(1); got != want {
		t.Errorf("ByteStream.Write calls = %d, want %d", got, want)
	}
	if got, want := fake.bsReadCalls.Load(), int64(1); got != want {
		t.Errorf("ByteStream.Read calls = %d, want %d", got, want)
	}
	fake.mu.Lock()
	for name, got := range map[string]repb.DigestFunction_Value{
		"FindMissingBlobs": fake.lastFindMissingFn,
		"BatchUpdateBlobs": fake.lastBatchUpdateFn,
		"BatchReadBlobs":   fake.lastBatchReadFn,
		"Execute":          fake.lastExecuteFn,
	} {
		if want := repb.DigestFunction_BLAKE3; got != want {
			t.Errorf("%s carried digest function %v, want %v", name, got, want)
		}
	}
	fake.mu.Unlock()

	// Evict an input upstream and re-run: recovery must parse the
	// segmented BLAKE3 missing-blob Subject and re-upload from the local
	// CAS under BLAKE3.
	evicted := a.CommandDigest
	fake.mu.Lock()
	delete(fake.blobs, evicted)
	fake.mu.Unlock()
	fake.executeOps = missingOnCalls(t, blake3, []digest.Digest{evicted}, 2)
	if _, err := executor.Execute(a); err != nil {
		t.Fatalf("Execute with blake3 eviction recovery: %v", err)
	}
	fake.mu.Lock()
	_, ok := fake.blobs[evicted]
	fake.mu.Unlock()
	if got, want := ok, true; got != want {
		t.Errorf("evicted blob is upstream again = %t, want %t", got, want)
	}
}

// blobOfSize returns size bytes, at least 8, that start with prefix and i, so
// that different arguments give different content.
func blobOfSize(prefix byte, i, size int) []byte {
	b := fmt.Appendf(nil, "%c%d:", prefix, i)
	return append(b, fillBytes(prefix, size-len(b))...)
}

// TestExecuteSplitsTransfers verifies, for uploads and downloads alike,
// which RPC carries each blob: blobs of at least 2 MiB (byteStreamThreshold)
// or over the upstream's batch limit use ByteStream, and the others share
// batch requests that each stay within the upstream's limit capped at 3 MiB
// (defaultMaxBatchSize), and within MaxBatchBlobs blobs. Small blobs that fit
// in one request share it, which saves round trips.
func TestExecuteSplitsTransfers(t *testing.T) {
	const kib = 1024
	tests := []struct {
		name string

		// upstreamLimit is the MaxBatchTotalSizeBytes the upstream
		// advertises and enforces, or 0 for the fake's default.
		upstreamLimit int64

		// sizes are the sizes of the action's input files, and also of the
		// output files the upstream returns.
		sizes []int

		// wantBatchLimit is the payload limit every batch request must
		// respect.
		wantBatchLimit int64

		// wantByteStream is the number of blobs in each direction that
		// must go through ByteStream.
		wantByteStream int64

		// wantMinBatches is the least number of batch requests in each
		// direction.
		wantMinBatches int

		// wantMaxUpdates and wantMaxReads are the most BatchUpdateBlobs and
		// BatchReadBlobs requests, or 0 for no bound. Uploads have no bound
		// in most cases, because the proxy uploads the Action, Command and
		// input root too, in no fixed order.
		wantMaxUpdates int
		wantMaxReads   int
	}{
		{
			// The blobs and the Action, Command and input root fit in one
			// request each way, with room to spare.
			name:           "small_blobs_share_one_batch",
			sizes:          []int{100, 200, 300},
			wantBatchLimit: 3 * 1024 * 1024,
			wantByteStream: 0,
			wantMinBatches: 1,
			wantMaxUpdates: 1,
			wantMaxReads:   1,
		},
		{
			// The outputs download in result order: 600 and 300 bytes fill
			// the first request, and the two outputs of 200 bytes share the
			// second.
			name:           "second_batch_takes_the_rest",
			upstreamLimit:  kib,
			sizes:          []int{600, 300, 200, 200},
			wantBatchLimit: kib,
			wantByteStream: 0,
			wantMinBatches: 2,
			wantMaxReads:   2,
		},
		{
			name:           "large_blobs_use_bytestream",
			upstreamLimit:  4 * 1024 * 1024,
			sizes:          []int{3 * 1024 * 1024, 100},
			wantBatchLimit: 3 * 1024 * 1024,
			wantByteStream: 1,
			wantMinBatches: 1,
		},
		{
			name:           "batches_split_at_upstream_limit",
			upstreamLimit:  kib,
			sizes:          []int{600, 600, 600, 2 * kib},
			wantBatchLimit: kib,
			wantByteStream: 1,
			wantMinBatches: 3,
		},
		{
			name:           "batches_capped_below_large_upstream_limit",
			upstreamLimit:  8 * 1024 * 1024,
			sizes:          []int{1600 * kib, 1600 * kib},
			wantBatchLimit: 3 * 1024 * 1024,
			wantByteStream: 0,
			wantMinBatches: 2,
		},
		{
			// The blobs are far below the size limit, but one batch may not
			// hold them all.
			name:           "batches_cap_blob_count",
			sizes:          slices.Repeat([]int{16}, upstreamexec.MaxBatchBlobs+1),
			wantBatchLimit: 3 * 1024 * 1024,
			wantByteStream: 0,
			wantMinBatches: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Stage an action with the input files, and make the upstream
			// return output files with different content.
			fake := newFakeUpstream(t, []byte("unused"))
			fake.capsMaxBatchSize = tc.upstreamLimit
			executor, cas := startProxy(t, fake)
			inputs := make(map[string][]byte, len(tc.sizes))
			outputs := make(map[string][]byte, len(tc.sizes))
			result := &repb.ActionResult{}
			fake.mu.Lock()
			for i, size := range tc.sizes {
				inputs[fmt.Sprintf("in%d", i)] = blobOfSize('i', i, size)
				name := fmt.Sprintf("out/%d", i)
				outputs[name] = blobOfSize('o', i, size)
				d := digest.SHA256.FromBytes(outputs[name])
				fake.blobs[d] = outputs[name]
				result.OutputFiles = append(result.OutputFiles, &repb.OutputFile{Path: name, Digest: d.Proto()})
			}
			fake.mu.Unlock()
			fake.executeOps = respondWith(t, &repb.ExecuteResponse{Result: result})
			ad := putActionWithInputs(t, cas, digest.SHA256, inputs)

			// Run it through the proxy.
			if _, err := executor.Execute(loadAction(t, cas, digest.SHA256, ad)); err != nil {
				t.Fatalf("Execute: %v", err)
			}

			// Check that every blob arrived on the other side.
			fake.mu.Lock()
			defer fake.mu.Unlock()
			for name, data := range inputs {
				if _, ok := fake.blobs[digest.SHA256.FromBytes(data)]; !ok {
					t.Errorf("input %s was not uploaded", name)
				}
			}
			for name, data := range outputs {
				if !cas.Has(digest.SHA256, digest.SHA256.FromBytes(data)) {
					t.Errorf("output %s was not downloaded", name)
				}
			}

			// Check which RPCs carried them.
			if got, want := fake.bsWriteCalls.Load(), tc.wantByteStream; got != want {
				t.Errorf("ByteStream.Write calls = %d, want %d", got, want)
			}
			if got, want := fake.bsReadCalls.Load(), tc.wantByteStream; got != want {
				t.Errorf("ByteStream.Read calls = %d, want %d", got, want)
			}
			for name, batches := range map[string]struct {
				sizes      []int64
				counts     []int
				maxBatches int
			}{
				"BatchUpdateBlobs": {fake.batchUpdateSizes, fake.batchUpdateCounts, tc.wantMaxUpdates},
				"BatchReadBlobs":   {fake.batchReadSizes, fake.batchReadCounts, tc.wantMaxReads},
			} {
				if got, want := len(batches.counts), tc.wantMinBatches; got < want {
					t.Errorf("%s requests = %d, want at least %d", name, got, want)
					continue
				}
				if got, want := len(batches.counts), batches.maxBatches; want > 0 && got > want {
					t.Errorf("%s requests = %d, want at most %d (blob counts: %v)", name, got, want, batches.counts)
				}
				if got, want := slices.Max(batches.sizes), tc.wantBatchLimit; got > want {
					t.Errorf("largest %s payload = %d, want <= %d (all: %v)", name, got, want, batches.sizes)
				}
				if got, want := slices.Max(batches.counts), upstreamexec.MaxBatchBlobs; got > want {
					t.Errorf("largest %s request has %d blobs, want <= %d", name, got, want)
				}
			}
		})
	}
}

// TestExecuteJudgesByteStreamUpload verifies how an upload through
// ByteStream judges an upstream that ends the write early, which REAPI
// allows when the blob already exists, or answers with an error or a wrong
// committed size.
func TestExecuteJudgesByteStreamUpload(t *testing.T) {
	tests := []struct {
		name string

		// writeResult is the upstream's answer to the write.
		writeResult func(d digest.Digest) (*bspb.WriteResponse, error)

		// wantErr tells whether Execute fails.
		wantErr bool
	}{
		{
			name: "commits_early",
			writeResult: func(d digest.Digest) (*bspb.WriteResponse, error) {
				return &bspb.WriteResponse{CommittedSize: d.SizeBytes}, nil
			},
		},
		{
			name: "already_exists",
			writeResult: func(digest.Digest) (*bspb.WriteResponse, error) {
				return nil, status.Error(codes.AlreadyExists, "blob exists")
			},
		},
		{
			name: "error_after_early_end",
			writeResult: func(digest.Digest) (*bspb.WriteResponse, error) {
				return nil, status.Error(codes.Internal, "disk full")
			},
			wantErr: true,
		},
		{
			name: "committed_size_mismatch",
			writeResult: func(d digest.Digest) (*bspb.WriteResponse, error) {
				return &bspb.WriteResponse{CommittedSize: d.SizeBytes - 1}, nil
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Stage an action with an input large enough that its write
			// takes several requests.
			fake := newFakeUpstream(t, []byte("hello\n"))
			fake.writeResult = tc.writeResult
			executor, cas := startProxy(t, fake)
			ad := putActionWithInputs(t, cas, digest.SHA256, map[string][]byte{
				"large": fillBytes('x', 8*1024*1024),
			})

			// Execute it and check the outcome of the single write.
			_, err := executor.Execute(loadAction(t, cas, digest.SHA256, ad))
			if got, want := err != nil, tc.wantErr; got != want {
				t.Errorf("Execute error = %v, want error: %t", err, want)
			}
			if got, want := fake.bsWriteCalls.Load(), int64(1); got != want {
				t.Errorf("ByteStream.Write calls = %d, want %d", got, want)
			}
		})
	}
}

// TestExecuteRejectsBadByteStreamDownload verifies that a ByteStream download
// of an output that doesn't match its digest fails Execute, and leaves
// neither the blob nor a staging file in the local CAS.
func TestExecuteRejectsBadByteStreamDownload(t *testing.T) {
	output := fillBytes('o', 3*1024*1024)
	corrupt := slices.Clone(output)
	corrupt[len(corrupt)/2] ^= 1
	tests := []struct {
		name string

		// served is what the upstream sends for the output.
		served []byte

		// readBreak, if non-nil, breaks the stream after the first chunk.
		readBreak error
	}{
		{name: "short", served: output[:len(output)-1]},
		{name: "too_long", served: append(slices.Clone(output), 'x')},
		{name: "corrupt", served: corrupt},
		{name: "stream_breaks", served: output, readBreak: status.Error(codes.Unavailable, "connection reset")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Make the upstream serve the case's data for the output.
			fake := newFakeUpstream(t, output)
			od := digest.SHA256.FromBytes(output)
			fake.blobs[od] = tc.served
			fake.readBreak = tc.readBreak
			casDir := filepath.Join(t.TempDir(), "cas")
			cas := newCAS(t, casDir, digest.SHA256)
			executor := newProxy(t, fake, cas, digest.SHA256)

			// Execute and check that it fails without leftovers.
			if _, err := executor.Execute(loadHelloAction(t, cas)); err == nil {
				t.Error("Execute succeeded, want error")
			}
			if got, want := cas.Has(digest.SHA256, od), false; got != want {
				t.Errorf("local CAS has the output = %t, want %t", got, want)
			}
			if got := stagingFiles(t, casDir); len(got) > 0 {
				t.Errorf("staging files = %q, want none", got)
			}
		})
	}
}

// TestExecuteDownloadsConcurrently verifies that two Execute calls can
// download the same output through ByteStream at once: when one stores the
// output while the other is still downloading it, both succeed, and no
// staging file is left behind.
func TestExecuteDownloadsConcurrently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Give the action an output large enough for ByteStream, and pause
		// the first download of it after its first chunk.
		output := fillBytes('o', 3*1024*1024)
		fake := newFakeUpstream(t, output)
		fake.readPause = make(chan struct{})
		casDir := filepath.Join(t.TempDir(), "cas")
		cas := newCAS(t, casDir, digest.SHA256)
		executor := newProxy(t, fake, cas, digest.SHA256)
		action := loadHelloAction(t, cas)

		// Start A and wait until its download pauses.
		errA := make(chan error, 1)
		go func() { _, err := executor.Execute(action); errA <- err }()
		synctest.Wait()

		// Run B, which stores the output, and then let A finish.
		if _, err := executor.Execute(action); err != nil {
			t.Errorf("Execute B: %v", err)
		}
		close(fake.readPause)
		if err := <-errA; err != nil {
			t.Errorf("Execute A: %v", err)
		}

		// Check the local CAS.
		if got, want := cas.Has(digest.SHA256, digest.SHA256.FromBytes(output)), true; got != want {
			t.Errorf("local CAS has the output = %t, want %t", got, want)
		}
		if got := stagingFiles(t, casDir); len(got) > 0 {
			t.Errorf("staging files = %q, want none", got)
		}
	})
}

// countingExecutor wraps another executor and counts how many times Execute
// is called. The counter lets a test assert that a frontend Kajiya served the
// second invocation from its local action cache (counter doesn't increment).
type countingExecutor struct {
	inner execution.ExecutorInterface
	count atomic.Int64
}

// Execute implements execution.ExecutorInterface.
func (c *countingExecutor) Execute(a *model.Action) (*repb.ActionResult, error) {
	c.count.Add(1)
	return c.inner.Execute(a)
}

// helloExecutor runs the actions that putActionWithInputs builds without a
// sandbox: it stores "hello\n" in its CAS and reports it as out/result.txt,
// which is what their command writes.
type helloExecutor struct {
	cas *blobstore.ContentAddressableStorage
}

// Execute implements execution.ExecutorInterface.
func (h *helloExecutor) Execute(a *model.Action) (*repb.ActionResult, error) {
	d, err := h.cas.Put(a.Fn, []byte("hello\n"))
	if err != nil {
		return nil, err
	}
	return &repb.ActionResult{
		OutputFiles: []*repb.OutputFile{{Path: "out/result.txt", Digest: d.Proto()}},
	}, nil
}

// loopback is a "frontend" Kajiya that proxies execution to an "upstream"
// Kajiya, both running in-process over bufconn.
type loopback struct {
	// casA, client and proxy are the frontend's CAS, a client of its
	// Execution service, and its upstreamexec.Executor.
	casA   *blobstore.ContentAddressableStorage
	client repb.ExecutionClient
	proxy  *upstreamexec.Executor

	// casB and upstreamCounter are the upstream's CAS and its executor,
	// which runs actions with helloExecutor.
	casB            *blobstore.ContentAddressableStorage
	upstreamCounter *countingExecutor

	// upstreamStats records how the frontend's calls to the upstream end.
	upstreamStats *rpcStats
}

// startLoopback starts a loopback with both Kajiyas configured for fn. The
// frontend has an action cache, and the upstream has none.
func startLoopback(t *testing.T, fn digest.Function) *loopback {
	t.Helper()
	ctx := t.Context()
	baseDir := t.TempDir()
	lb := &loopback{upstreamStats: &rpcStats{}}

	// Start server B, the upstream. It runs actions without an action
	// cache, and serves the CAS, ByteStream and Capabilities so that the
	// frontend can upload inputs and read outputs.
	lb.casB = newCAS(t, filepath.Join(baseDir, "casB"), fn)
	lb.upstreamCounter = &countingExecutor{inner: &helloExecutor{cas: lb.casB}}
	cfg := server.Config{DigestFunctions: []digest.Function{fn}}
	cfg.MaxRecvMsgSize = cfg.RecommendedMaxRecvMsgSize()
	srvB := grpc.NewServer(grpc.MaxRecvMsgSize(cfg.MaxRecvMsgSize))
	capabilities.Register(srvB, cfg)
	blobstore.Register(srvB, lb.casB, cfg)
	if err := execution.Register(srvB, lb.upstreamCounter, nil, lb.casB, cfg); err != nil {
		t.Fatalf("register upstream execution: %v", err)
	}
	lisB := bufconn.Listen(bufSize)
	go func() { _ = srvB.Serve(lisB) }()
	t.Cleanup(func() { srvB.Stop(); _ = lisB.Close() })

	// Connect the frontend to Server B.
	connToB, err := grpc.NewClient("passthrough://bufnetB",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lisB.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(lb.upstreamStats),
	)
	if err != nil {
		t.Fatalf("dial upstream: %v", err)
	}
	t.Cleanup(func() { _ = connToB.Close() })

	// Start server A, the frontend, with a CAS, an action cache and
	// execution proxied to server B.
	lb.casA = newCAS(t, filepath.Join(baseDir, "casA"), fn)
	acA, err := actioncache.NewWithOpts(ctx, filepath.Join(baseDir, "acA"), lb.casA, actioncache.Options{
		DigestFunctions: []digest.Function{fn},
	})
	if err != nil {
		t.Fatalf("create frontend AC: %v", err)
	}
	lb.proxy, err = upstreamexec.New(ctx, connToB, lb.casA, "", []digest.Function{fn})
	if err != nil {
		t.Fatalf("create frontend executor: %v", err)
	}
	srvA := grpc.NewServer(grpc.MaxRecvMsgSize(cfg.MaxRecvMsgSize))
	if err := execution.Register(srvA, lb.proxy, acA, lb.casA, cfg); err != nil {
		t.Fatalf("register frontend execution: %v", err)
	}
	lisA := bufconn.Listen(bufSize)
	go func() { _ = srvA.Serve(lisA) }()
	t.Cleanup(func() { srvA.Stop(); _ = lisA.Close() })

	// Connect a client to Server A.
	connToA, err := grpc.NewClient("passthrough://bufnetA",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lisA.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial frontend: %v", err)
	}
	t.Cleanup(func() { _ = connToA.Close() })
	lb.client = repb.NewExecutionClient(connToA)
	return lb
}

// TestExecuteThroughKajiyaUpstream runs an Action through a frontend Kajiya
// that proxies execution to an upstream Kajiya. It asserts: (1) the action
// runs successfully and the output bytes land in the frontend's local CAS;
// (2) a second call for the same action is served by the frontend's local
// action cache without reaching the upstream.
func TestExecuteThroughKajiyaUpstream(t *testing.T) {
	tests := []struct {
		name string
		fn   string
	}{
		{name: "sha256", fn: "sha256"},
		{name: "blake3", fn: "blake3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Start the loopback and build a trivial action: write "hello\n"
			// into out/result.txt.
			fn := fnsForTest(t, tc.fn)[0]
			lb := startLoopback(t, fn)
			actionDigest := putHelloAction(t, lb.casA, fn)

			// Run the action once, which must reach the upstream and produce
			// the right output.
			ar1 := runOnce(t, t.Context(), lb.client, fn, actionDigest)
			if got, want := ar1.ExitCode, int32(0); got != want {
				t.Fatalf("first call: exit code = %d, want %d", got, want)
			}
			if got, want := lb.upstreamCounter.count.Load(), int64(1); got != want {
				t.Errorf("upstream call count after first invocation = %d, want %d", got, want)
			}

			// Verify output content is in the frontend CAS.
			if got, want := len(ar1.OutputFiles), 1; got != want {
				t.Fatalf("got %d output files, want %d: %+v", got, want, ar1.OutputFiles)
			}
			if got, want := ar1.OutputFiles[0].Path, "out/result.txt"; got != want {
				t.Fatalf("output file path = %q, want %q", got, want)
			}
			outDigest, err := fn.FromProto(ar1.OutputFiles[0].Digest)
			if err != nil {
				t.Fatalf("parse output digest: %v", err)
			}
			got, err := lb.casA.Get(fn, outDigest)
			if err != nil {
				t.Fatalf("read output from frontend CAS: %v", err)
			}
			if got, want := string(got), "hello\n"; got != want {
				t.Errorf("output content = %q, want %q", got, want)
			}

			// Run it again, which the frontend's local action cache must
			// serve without reaching the upstream.
			ar2 := runOnce(t, t.Context(), lb.client, fn, actionDigest)
			if got, want := ar2.ExitCode, int32(0); got != want {
				t.Fatalf("second call: exit code = %d, want %d", got, want)
			}
			if got, want := lb.upstreamCounter.count.Load(), int64(1); got != want {
				t.Errorf("upstream call count after second invocation = %d, want %d (cache hit expected)", got, want)
			}
			if got, want := ar2.OutputFiles[0].GetDigest(), ar1.OutputFiles[0].GetDigest(); !proto.Equal(got, want) {
				t.Errorf("output digest of second run = %v, want %v", got, want)
			}
		})
	}
}

// TestExecuteRestoresInputsKajiyaReportsMissing verifies recovery from a
// Kajiya upstream, which reports missing inputs as the error of the Execute
// call, one level of the input tree per call. After the upstream lost every
// input, whose presence the proxy had memoized, one retry suffices: the proxy
// restores all of the inputs, not only the reported Action.
func TestExecuteRestoresInputsKajiyaReportsMissing(t *testing.T) {
	// Run an action with an input file once through a proxy in front of a
	// Kajiya upstream.
	lb := startLoopback(t, digest.SHA256)
	input := []byte("input\n")
	ad := putActionWithInputs(t, lb.casA, digest.SHA256, map[string][]byte{"in.txt": input})
	action := loadAction(t, lb.casA, digest.SHA256, ad)
	if _, err := lb.proxy.Execute(action); err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	// Make the upstream lose every input: the Action, the Command, the
	// input root, and the input file are four levels for it to report.
	for _, d := range []digest.Digest{ad, action.CommandDigest, action.InputRootDigest, digest.SHA256.FromBytes(input)} {
		if err := lb.casB.Delete(digest.SHA256, d); err != nil {
			t.Fatalf("delete %s from upstream CAS: %v", d, err)
		}
	}

	// Execute again and check that it succeeds after one retry.
	if _, err := lb.proxy.Execute(action); err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	want := []codes.Code{codes.OK, codes.FailedPrecondition, codes.OK}
	if got := lb.upstreamStats.ends(executeMethod); !slices.Equal(got, want) {
		t.Errorf("upstream Execute calls ended with %v, want %v (first run, failed attempt, one retry)", got, want)
	}
}

// runOnce sends a single Execute RPC and returns the final ActionResult.
func runOnce(t *testing.T, ctx context.Context, client repb.ExecutionClient, fn digest.Function, ad digest.Digest) *repb.ActionResult {
	t.Helper()
	stream, err := client.Execute(ctx, &repb.ExecuteRequest{
		ActionDigest:   ad.Proto(),
		DigestFunction: fn.Value(),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for {
		op, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			t.Fatal("Execute stream ended without a Done operation")
		}
		if err != nil {
			t.Fatalf("Execute recv: %v", err)
		}
		if !op.Done {
			continue
		}
		resp := &repb.ExecuteResponse{}
		if err := op.GetResponse().UnmarshalTo(resp); err != nil {
			t.Fatalf("unmarshal ExecuteResponse: %v", err)
		}
		if resp.Result == nil {
			t.Fatalf("ExecuteResponse missing Result")
		}
		return resp.Result
	}
}

// putHelloAction builds and uploads a trivial REAPI action that runs
// `/bin/sh -c "echo hello > out/result.txt"` with `out/` as an empty
// subdirectory in the input root, then returns the action digest.
func putHelloAction(t *testing.T, cas *blobstore.ContentAddressableStorage, fn digest.Function) digest.Digest {
	t.Helper()
	return putActionWithInputs(t, cas, fn, nil)
}

// putActionWithInputs is putHelloAction with additional input files at the
// root of the input root, keyed by name.
func putActionWithInputs(t *testing.T, cas *blobstore.ContentAddressableStorage, fn digest.Function, inputs map[string][]byte) digest.Digest {
	t.Helper()

	putProto := func(m proto.Message) *repb.Digest {
		t.Helper()
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatalf("marshal proto: %v", err)
		}
		d, err := cas.Put(fn, b)
		if err != nil {
			t.Fatalf("put proto: %v", err)
		}
		return d.Proto()
	}

	// Store an empty out/ subdirectory.
	outDirDigest := putProto(&repb.Directory{})

	// Store the input root, which holds the input files, sorted by name,
	// and out/.
	var files []*repb.FileNode
	for _, name := range slices.Sorted(maps.Keys(inputs)) {
		d, err := cas.Put(fn, inputs[name])
		if err != nil {
			t.Fatalf("put input %s: %v", name, err)
		}
		files = append(files, &repb.FileNode{Name: name, Digest: d.Proto()})
	}
	rootDigest := putProto(&repb.Directory{
		Files: files,
		Directories: []*repb.DirectoryNode{
			{Name: "out", Digest: outDirDigest},
		},
	})

	// Store a Command that writes "hello" into out/result.txt.
	cmdDigest := putProto(&repb.Command{
		Arguments:   []string{"/bin/sh", "-c", "echo hello > out/result.txt"},
		OutputPaths: []string{"out/result.txt"},
	})

	// Store the Action, which references both protos. DoNotCache stays false
	// so the second call can hit the frontend's local action cache.
	actionPb := putProto(&repb.Action{
		CommandDigest:   cmdDigest,
		InputRootDigest: rootDigest,
	})
	ad, err := fn.FromProto(actionPb)
	if err != nil {
		t.Fatalf("parse action digest: %v", err)
	}
	return ad
}
