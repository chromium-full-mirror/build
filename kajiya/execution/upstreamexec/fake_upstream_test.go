// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package upstreamexec_test

import (
	"context"
	"net"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/google/uuid"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	errdetailpb "google.golang.org/genproto/googleapis/rpc/errdetails"
	rpcstatuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/anypb"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/upstreamexec"
)

// fakeInstance is the instance name that the proxies in these tests forward,
// and that the fake upstream requires of every request. It has several
// segments, as RBE's instance names do.
const fakeInstance = "projects/fake/instances/default_instance"

// fakeUpstreamMaxBatchSize is the batch limit the fake upstream advertises
// unless a test sets fakeUpstream.capsMaxBatchSize.
const fakeUpstreamMaxBatchSize = 4 * 1024 * 1024

// fakeReadChunkSize is the payload size of each ByteStream.Read response the
// fake upstream sends.
const fakeReadChunkSize = 1024 * 1024

// executeMethod is the full gRPC method name of Execute.
const executeMethod = "/build.bazel.remote.execution.v2.Execution/Execute"

// fakeUpstream implements the REAPI server-side surface upstreamexec talks
// to: Capabilities, CAS, ByteStream, and an Execution service that returns a
// synthetic result without running anything. Tests use it to count and gate
// RPCs.
//
// The fake is strict about requests: each one must name fakeInstance, a
// ByteStream resource name must have exactly the form REAPI defines under an
// advertised digest function, and FindMissingBlobs rejects a request that
// lists a digest twice. Its CAS is an in-memory map keyed by digest, and
// batch requests over the advertised batch limit fail, as they would on a
// real server.
type fakeUpstream struct {
	repb.UnimplementedCapabilitiesServer
	repb.UnimplementedContentAddressableStorageServer
	repb.UnimplementedExecutionServer
	bspb.UnimplementedByteStreamServer

	mu    sync.Mutex
	blobs map[digest.Digest][]byte

	findMissingCalls atomic.Int64
	batchUpdateCalls atomic.Int64
	batchReadCalls   atomic.Int64
	bsWriteCalls     atomic.Int64
	bsReadCalls      atomic.Int64
	executeCalls     atomic.Int64

	// batchUpdateSizes and batchReadSizes record the total blob payload of
	// each BatchUpdateBlobs and BatchReadBlobs request, and
	// batchUpdateCounts and batchReadCounts the number of blobs in it.
	// Protected by mu.
	batchUpdateSizes  []int64
	batchReadSizes    []int64
	batchUpdateCounts []int
	batchReadCounts   []int

	// batchUpdateGate, if non-nil, makes BatchUpdateBlobs block until it is
	// closed.
	batchUpdateGate chan struct{}

	// writeResult, if non-nil, makes ByteStream.Write end every write after
	// its first request with the response and error that writeResult returns
	// for the blob, without storing the blob.
	writeResult func(d digest.Digest) (*bspb.WriteResponse, error)

	// readPause, if non-nil, makes the first ByteStream.Read pause after its
	// first chunk until readPause is closed.
	readPause chan struct{}

	// readBreak, if non-nil, makes ByteStream.Read fail with it after the
	// first chunk.
	readBreak error

	// outputDigest references the OutputFile blob that the default result of
	// Execute references. Stored in fake.blobs at construction time.
	outputDigest *repb.Digest

	// executeOps, if non-nil, returns the operations that the call-th Execute
	// call, counting from 1, sends for req, or nil to send the default: a
	// single Done operation whose result references outputDigest. It runs
	// without mu held, and may block.
	executeOps func(call int64, req *repb.ExecuteRequest) []*longrunningpb.Operation

	// holdExecuteStream makes Execute keep the stream open after its last
	// operation, until the client cancels it.
	holdExecuteStream bool

	// lastExecuteSkipCacheLookup records the SkipCacheLookup value of the
	// most recent ExecuteRequest. Protected by mu.
	lastExecuteSkipCacheLookup bool

	// lastFindMissingFn, lastBatchUpdateFn, lastBatchReadFn and
	// lastExecuteFn record the DigestFunction of the most recent request of
	// each type. Protected by mu.
	lastFindMissingFn repb.DigestFunction_Value
	lastBatchUpdateFn repb.DigestFunction_Value
	lastBatchReadFn   repb.DigestFunction_Value
	lastExecuteFn     repb.DigestFunction_Value

	// capsUnimplemented makes GetCapabilities return Unimplemented,
	// mimicking minimal REAPI servers without a Capabilities service.
	capsUnimplemented bool

	// capsMaxBatchSize is the MaxBatchTotalSizeBytes advertised by
	// GetCapabilities and enforced on batch requests. Zero means
	// fakeUpstreamMaxBatchSize.
	capsMaxBatchSize int64

	// capsDigestFunctions is the supported digest function set advertised
	// by GetCapabilities in the cache capabilities. Defaults to SHA-256.
	capsDigestFunctions []repb.DigestFunction_Value

	// capsExecDigestFunctions, capsExecDigestFunction and capsExecDisabled
	// control the ExecutionCapabilities returned by GetCapabilities: the
	// repeated set, the deprecated singular field, and an explicit
	// exec_enabled=false. ExecutionCapabilities is present when any of
	// them is set.
	capsExecDigestFunctions []repb.DigestFunction_Value
	capsExecDigestFunction  repb.DigestFunction_Value
	capsExecDisabled        bool

	// stats records how each RPC on the proxy's connection to the fake ends.
	stats rpcStats
}

// newFakeUpstream returns a fake upstream whose CAS holds outputBytes, the
// content of the output file its default Execute result references.
func newFakeUpstream(t *testing.T, outputBytes []byte) *fakeUpstream {
	t.Helper()
	od := digest.SHA256.FromBytes(outputBytes)
	f := &fakeUpstream{
		blobs:        map[digest.Digest][]byte{od: outputBytes},
		outputDigest: od.Proto(),
	}
	return f
}

// checkInstance fails a request whose instance name isn't fakeInstance.
func checkInstance(name string) error {
	if name != fakeInstance {
		return status.Errorf(codes.InvalidArgument, "instance_name = %q, want %q", name, fakeInstance)
	}
	return nil
}

// checkFunction fails a ByteStream request under a digest function that the
// fake doesn't advertise for its cache.
func (f *fakeUpstream) checkFunction(fn digest.Function) error {
	fns := f.capsDigestFunctions
	if len(fns) == 0 {
		fns = []repb.DigestFunction_Value{repb.DigestFunction_SHA256}
	}
	if !slices.Contains(fns, fn.Value()) {
		return status.Errorf(codes.InvalidArgument, "digest function %s not supported", fn)
	}
	return nil
}

// maxBatchSize returns the batch limit the fake advertises and enforces.
func (f *fakeUpstream) maxBatchSize() int64 {
	if f.capsMaxBatchSize > 0 {
		return f.capsMaxBatchSize
	}
	return fakeUpstreamMaxBatchSize
}

// GetCapabilities implements repb.CapabilitiesServer.
func (f *fakeUpstream) GetCapabilities(ctx context.Context, req *repb.GetCapabilitiesRequest) (*repb.ServerCapabilities, error) {
	if f.capsUnimplemented {
		return nil, status.Error(codes.Unimplemented, "no capabilities service")
	}
	if err := checkInstance(req.InstanceName); err != nil {
		return nil, err
	}
	fns := f.capsDigestFunctions
	if len(fns) == 0 {
		fns = []repb.DigestFunction_Value{repb.DigestFunction_SHA256}
	}
	sc := &repb.ServerCapabilities{
		CacheCapabilities: &repb.CacheCapabilities{
			DigestFunctions:        fns,
			MaxBatchTotalSizeBytes: f.maxBatchSize(),
		},
	}
	if len(f.capsExecDigestFunctions) > 0 || f.capsExecDigestFunction != repb.DigestFunction_UNKNOWN || f.capsExecDisabled {
		sc.ExecutionCapabilities = &repb.ExecutionCapabilities{
			ExecEnabled:     !f.capsExecDisabled,
			DigestFunction:  f.capsExecDigestFunction,
			DigestFunctions: f.capsExecDigestFunctions,
		}
	}
	return sc, nil
}

// FindMissingBlobs implements repb.ContentAddressableStorageServer.
func (f *fakeUpstream) FindMissingBlobs(ctx context.Context, req *repb.FindMissingBlobsRequest) (*repb.FindMissingBlobsResponse, error) {
	f.findMissingCalls.Add(1)
	if err := checkInstance(req.InstanceName); err != nil {
		return nil, err
	}
	resp := &repb.FindMissingBlobsResponse{}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastFindMissingFn = req.DigestFunction
	seen := make(map[digest.Digest]bool, len(req.BlobDigests))
	for _, pd := range req.BlobDigests {
		d, err := digest.SHA256.FromProto(pd)
		if err != nil {
			return nil, err
		}
		if seen[d] {
			return nil, status.Errorf(codes.InvalidArgument, "digest %s listed twice", d)
		}
		seen[d] = true
		if _, ok := f.blobs[d]; !ok {
			resp.MissingBlobDigests = append(resp.MissingBlobDigests, pd)
		}
	}
	return resp, nil
}

// BatchUpdateBlobs implements repb.ContentAddressableStorageServer. It
// blocks on batchUpdateGate if one is set.
func (f *fakeUpstream) BatchUpdateBlobs(ctx context.Context, req *repb.BatchUpdateBlobsRequest) (*repb.BatchUpdateBlobsResponse, error) {
	f.batchUpdateCalls.Add(1)
	if err := checkInstance(req.InstanceName); err != nil {
		return nil, err
	}
	if f.batchUpdateGate != nil {
		<-f.batchUpdateGate
	}

	// Enforce the advertised batch limit.
	var size int64
	for _, r := range req.Requests {
		size += int64(len(r.Data))
	}
	if size > f.maxBatchSize() {
		return nil, status.Errorf(codes.InvalidArgument, "batch of %d bytes exceeds limit %d", size, f.maxBatchSize())
	}

	// Store the blobs.
	resp := &repb.BatchUpdateBlobsResponse{
		Responses: make([]*repb.BatchUpdateBlobsResponse_Response, 0, len(req.Requests)),
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastBatchUpdateFn = req.DigestFunction
	f.batchUpdateSizes = append(f.batchUpdateSizes, size)
	f.batchUpdateCounts = append(f.batchUpdateCounts, len(req.Requests))
	for _, r := range req.Requests {
		d, err := digest.SHA256.FromProto(r.Digest)
		if err != nil {
			return nil, err
		}
		f.blobs[d] = r.Data
		resp.Responses = append(resp.Responses, &repb.BatchUpdateBlobsResponse_Response{
			Digest: r.Digest,
			Status: &rpcstatuspb.Status{Code: int32(codes.OK)},
		})
	}
	return resp, nil
}

// BatchReadBlobs implements repb.ContentAddressableStorageServer.
func (f *fakeUpstream) BatchReadBlobs(ctx context.Context, req *repb.BatchReadBlobsRequest) (*repb.BatchReadBlobsResponse, error) {
	f.batchReadCalls.Add(1)
	if err := checkInstance(req.InstanceName); err != nil {
		return nil, err
	}

	// Enforce the advertised batch limit.
	var size int64
	for _, pd := range req.Digests {
		size += pd.GetSizeBytes()
	}
	if size > f.maxBatchSize() {
		return nil, status.Errorf(codes.InvalidArgument, "batch of %d bytes exceeds limit %d", size, f.maxBatchSize())
	}

	// Look up the blobs.
	resp := &repb.BatchReadBlobsResponse{
		Responses: make([]*repb.BatchReadBlobsResponse_Response, 0, len(req.Digests)),
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastBatchReadFn = req.DigestFunction
	f.batchReadSizes = append(f.batchReadSizes, size)
	f.batchReadCounts = append(f.batchReadCounts, len(req.Digests))
	for _, pd := range req.Digests {
		d, err := digest.SHA256.FromProto(pd)
		if err != nil {
			return nil, err
		}
		data, ok := f.blobs[d]
		entry := &repb.BatchReadBlobsResponse_Response{Digest: pd}
		if !ok {
			entry.Status = &rpcstatuspb.Status{Code: int32(codes.NotFound)}
		} else {
			entry.Data = data
			entry.Status = &rpcstatuspb.Status{Code: int32(codes.OK)}
		}
		resp.Responses = append(resp.Responses, entry)
	}
	return resp, nil
}

// parseResourceName parses a ByteStream resource name of the form
// "{instance}/blobs/{digest_function/}{hash}/{size}", or, if upload is set,
// "{instance}/uploads/{uuid}/blobs/{digest_function/}{hash}/{size}". The
// instance must be fakeInstance, and the {digest_function} segment must be
// present exactly when the function has one. Without it, the function is
// SHA-256.
func parseResourceName(name string, upload bool) (digest.Function, digest.Digest, error) {
	bad := func(why string) error {
		return status.Errorf(codes.InvalidArgument, "bad resource name %q: %s", name, why)
	}

	// Strip the instance and, for an upload, the upload ID.
	rest, ok := strings.CutPrefix(name, fakeInstance+"/")
	if !ok {
		return digest.Function{}, digest.Digest{}, bad("wrong instance")
	}
	if upload {
		if rest, ok = strings.CutPrefix(rest, "uploads/"); !ok {
			return digest.Function{}, digest.Digest{}, bad(`no "uploads" segment`)
		}
		id, after, _ := strings.Cut(rest, "/")
		if _, err := uuid.Parse(id); err != nil {
			return digest.Function{}, digest.Digest{}, bad(err.Error())
		}
		rest = after
	}
	if rest, ok = strings.CutPrefix(rest, "blobs/"); !ok {
		return digest.Function{}, digest.Digest{}, bad(`no "blobs" segment`)
	}
	parts := strings.Split(rest, "/")

	// Parse the digest function segment.
	fn := digest.SHA256
	if len(parts) == 3 {
		var err error
		if fn, err = digest.ParseFunction(parts[0]); err != nil {
			return digest.Function{}, digest.Digest{}, bad(err.Error())
		}
		if got, want := parts[0], fn.ResourceNameSegment(); got != want {
			return digest.Function{}, digest.Digest{}, bad("unexpected digest function segment")
		}
		parts = parts[1:]
	}
	if len(parts) != 2 {
		return digest.Function{}, digest.Digest{}, bad("wrong number of segments")
	}

	// Parse the digest.
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return digest.Function{}, digest.Digest{}, bad(err.Error())
	}
	d, err := fn.Validate(parts[0], size)
	if err != nil {
		return digest.Function{}, digest.Digest{}, bad(err.Error())
	}
	return fn, d, nil
}

// Read implements bspb.ByteStreamServer, sending the blob in chunks of
// fakeReadChunkSize bytes. readPause and readBreak take effect after the
// first chunk.
func (f *fakeUpstream) Read(req *bspb.ReadRequest, stream bspb.ByteStream_ReadServer) error {
	call := f.bsReadCalls.Add(1)
	fn, d, err := parseResourceName(req.ResourceName, false)
	if err != nil {
		return err
	}
	if err := f.checkFunction(fn); err != nil {
		return err
	}
	f.mu.Lock()
	data, ok := f.blobs[d]
	f.mu.Unlock()
	if !ok {
		return status.Errorf(codes.NotFound, "blob %s not found", d)
	}
	for i, chunk := range slices.Collect(slices.Chunk(data, fakeReadChunkSize)) {
		if i == 1 && f.readPause != nil && call == 1 {
			<-f.readPause
		}
		if i == 1 && f.readBreak != nil {
			return f.readBreak
		}
		if err := stream.Send(&bspb.ReadResponse{Data: chunk}); err != nil {
			return err
		}
	}
	return nil
}

// Write implements bspb.ByteStreamServer. It checks the resource name, the
// write offsets and the digest of the data, and stores the blob once the
// client finishes the write, unless writeResult is set.
func (f *fakeUpstream) Write(stream bspb.ByteStream_WriteServer) error {
	f.bsWriteCalls.Add(1)

	// Read the first request, which names the blob.
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	resourceName := req.ResourceName
	fn, d, err := parseResourceName(resourceName, true)
	if err != nil {
		return err
	}
	if err := f.checkFunction(fn); err != nil {
		return err
	}
	if f.writeResult != nil {
		resp, err := f.writeResult(d)
		if err != nil {
			return err
		}
		return stream.SendAndClose(resp)
	}

	// Collect the data up to the request that finishes the write. A later
	// request may repeat the resource name, but not change it.
	var data []byte
	for {
		if got, want := req.WriteOffset, int64(len(data)); got != want {
			return status.Errorf(codes.InvalidArgument, "write_offset = %d, want %d", got, want)
		}
		if req.ResourceName != "" && req.ResourceName != resourceName {
			return status.Errorf(codes.InvalidArgument, "resource_name changed to %q", req.ResourceName)
		}
		data = append(data, req.Data...)
		if req.FinishWrite {
			break
		}
		if req, err = stream.Recv(); err != nil {
			return err
		}
	}

	// Verify and store the blob.
	if got := fn.FromBytes(data); got != d {
		return status.Errorf(codes.InvalidArgument, "uploaded data has digest %s, want %s", got, d)
	}
	f.mu.Lock()
	f.blobs[d] = data
	f.mu.Unlock()
	return stream.SendAndClose(&bspb.WriteResponse{CommittedSize: d.SizeBytes})
}

// Execute implements repb.ExecutionServer. It sends the operations that
// executeOps returns, by default a single Done operation whose result
// references outputDigest, and keeps the stream open afterwards if
// holdExecuteStream is set.
func (f *fakeUpstream) Execute(req *repb.ExecuteRequest, stream repb.Execution_ExecuteServer) error {
	call := f.executeCalls.Add(1)
	if err := checkInstance(req.InstanceName); err != nil {
		return err
	}

	// Record the request.
	f.mu.Lock()
	f.lastExecuteSkipCacheLookup = req.SkipCacheLookup
	f.lastExecuteFn = req.DigestFunction
	executeOps := f.executeOps
	f.mu.Unlock()

	// Pick the operations.
	var ops []*longrunningpb.Operation
	if executeOps != nil {
		ops = executeOps(call, req)
	}
	if ops == nil {
		op, err := newDoneOperation(&repb.ExecuteResponse{
			Result: &repb.ActionResult{
				OutputFiles: []*repb.OutputFile{
					{Path: "out/result.txt", Digest: f.outputDigest},
				},
			},
		})
		if err != nil {
			return err
		}
		ops = []*longrunningpb.Operation{op}
	}

	// Send them.
	for _, op := range ops {
		if err := stream.Send(op); err != nil {
			return err
		}
	}
	if f.holdExecuteStream {
		<-stream.Context().Done()
	}
	return nil
}

// newDoneOperation wraps resp in a Done Operation, as an upstream's Execute
// stream ends.
func newDoneOperation(resp *repb.ExecuteResponse) (*longrunningpb.Operation, error) {
	respAny, err := anypb.New(resp)
	if err != nil {
		return nil, err
	}
	return &longrunningpb.Operation{
		Name:   "fake-upstream/op",
		Done:   true,
		Result: &longrunningpb.Operation_Response{Response: respAny},
	}, nil
}

// doneOperation is newDoneOperation that fails the test on error.
func doneOperation(t *testing.T, resp *repb.ExecuteResponse) *longrunningpb.Operation {
	t.Helper()
	op, err := newDoneOperation(resp)
	if err != nil {
		t.Fatalf("anypb.New: %v", err)
	}
	return op
}

// respondWith returns an executeOps function that answers every Execute call
// with a Done operation that carries resp.
func respondWith(t *testing.T, resp *repb.ExecuteResponse) func(int64, *repb.ExecuteRequest) []*longrunningpb.Operation {
	t.Helper()
	ops := []*longrunningpb.Operation{doneOperation(t, resp)}
	return func(int64, *repb.ExecuteRequest) []*longrunningpb.Operation { return ops }
}

// preconditionStatus returns a FAILED_PRECONDITION status with a
// PreconditionFailure violation of the given type for each subject.
func preconditionStatus(t *testing.T, violationType string, subjects ...string) *rpcstatuspb.Status {
	t.Helper()

	// Build a violation per subject.
	violations := make([]*errdetailpb.PreconditionFailure_Violation, 0, len(subjects))
	for _, subject := range subjects {
		violations = append(violations, &errdetailpb.PreconditionFailure_Violation{
			Type:        violationType,
			Subject:     subject,
			Description: "reported by fake upstream",
		})
	}

	// Wrap them in the status.
	pfAny, err := anypb.New(&errdetailpb.PreconditionFailure{Violations: violations})
	if err != nil {
		t.Fatalf("anypb.New: %v", err)
	}
	return &rpcstatuspb.Status{
		Code:    int32(codes.FailedPrecondition),
		Message: "precondition failed",
		Details: []*anypb.Any{pfAny},
	}
}

// missingBlobsStatus returns the FAILED_PRECONDITION status with a MISSING
// violation per blob that REAPI servers use to report missing inputs under
// fn. The subjects carry the {digest_function} segment for functions that
// have one, as Buildbarn and Buildfarm send them.
func missingBlobsStatus(t *testing.T, fn digest.Function, blobs ...digest.Digest) *rpcstatuspb.Status {
	t.Helper()
	subjects := make([]string, 0, len(blobs))
	for _, d := range blobs {
		subjects = append(subjects, path.Join("blobs", fn.ResourceNameSegment(), d.Hash, strconv.FormatInt(d.SizeBytes, 10)))
	}
	return preconditionStatus(t, "MISSING", subjects...)
}

// missingOnCalls returns an executeOps function that reports blobs missing
// under fn on the given Execute calls, and sends the default operations on
// the others.
func missingOnCalls(t *testing.T, fn digest.Function, blobs []digest.Digest, calls ...int64) func(int64, *repb.ExecuteRequest) []*longrunningpb.Operation {
	t.Helper()
	ops := []*longrunningpb.Operation{doneOperation(t, &repb.ExecuteResponse{Status: missingBlobsStatus(t, fn, blobs...)})}
	return func(call int64, _ *repb.ExecuteRequest) []*longrunningpb.Operation {
		if slices.Contains(calls, call) {
			return ops
		}
		return nil
	}
}

// rpcStats is a client-side stats.Handler that records the final status code
// of every RPC, by full method name.
type rpcStats struct {
	mu    sync.Mutex
	codes map[string][]codes.Code
}

// methodKey is the context key under which rpcStats tags an RPC with its
// full method name.
type methodKey struct{}

// TagRPC implements stats.Handler.
func (s *rpcStats) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	return context.WithValue(ctx, methodKey{}, info.FullMethodName)
}

// HandleRPC implements stats.Handler.
func (s *rpcStats) HandleRPC(ctx context.Context, rs stats.RPCStats) {
	end, ok := rs.(*stats.End)
	if !ok {
		return
	}
	method, _ := ctx.Value(methodKey{}).(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.codes == nil {
		s.codes = make(map[string][]codes.Code)
	}
	s.codes[method] = append(s.codes[method], status.Code(end.Error))
}

// TagConn implements stats.Handler.
func (s *rpcStats) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

// HandleConn implements stats.Handler.
func (s *rpcStats) HandleConn(context.Context, stats.ConnStats) {}

// ends returns the final status codes of the calls of method that ended so
// far, in order.
func (s *rpcStats) ends(method string) []codes.Code {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.codes[method])
}

// dialFakeUpstream boots the fake upstream over bufconn and returns a client
// connection to it, which records its RPCs in fake.stats.
func dialFakeUpstream(t *testing.T, fake *fakeUpstream) *grpc.ClientConn {
	t.Helper()

	// Serve the fake.
	srv := grpc.NewServer(grpc.MaxRecvMsgSize(bufSize))
	repb.RegisterCapabilitiesServer(srv, fake)
	repb.RegisterContentAddressableStorageServer(srv, fake)
	repb.RegisterExecutionServer(srv, fake)
	bspb.RegisterByteStreamServer(srv, fake)
	lis := bufconn.Listen(bufSize)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Stop(); _ = lis.Close() })

	// Connect to it.
	conn, err := grpc.NewClient("passthrough://fake",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(&fake.stats),
	)
	if err != nil {
		t.Fatalf("dial fake upstream: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// newCAS returns an empty local CAS in dir for the digest functions fns.
func newCAS(t *testing.T, dir string, fns ...digest.Function) *blobstore.ContentAddressableStorage {
	t.Helper()
	cas, err := blobstore.NewWithOpts(t.Context(), dir, blobstore.Options{DigestFunctions: fns})
	if err != nil {
		t.Fatalf("create local CAS: %v", err)
	}
	return cas
}

// newProxy boots the fake upstream over bufconn and returns an Executor that
// forwards fakeInstance to it under the digest functions fns, backed by cas.
func newProxy(t *testing.T, fake *fakeUpstream, cas *blobstore.ContentAddressableStorage, fns ...digest.Function) *upstreamexec.Executor {
	t.Helper()
	executor, err := upstreamexec.New(t.Context(), dialFakeUpstream(t, fake), cas, fakeInstance, fns)
	if err != nil {
		t.Fatalf("create executor: %v", err)
	}
	return executor
}

// startProxy returns a SHA-256 Executor in front of the fake upstream,
// backed by a fresh local CAS, and that CAS.
func startProxy(t *testing.T, fake *fakeUpstream) (*upstreamexec.Executor, *blobstore.ContentAddressableStorage) {
	t.Helper()
	cas := newCAS(t, filepath.Join(t.TempDir(), "cas"), digest.SHA256)
	return newProxy(t, fake, cas, digest.SHA256), cas
}
