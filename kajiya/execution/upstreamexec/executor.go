// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package upstreamexec implements an execution backend that forwards the
// REAPI Execute call to an upstream REAPI service, such as Google RBE,
// instead of running the action locally.
//
// The executor uploads any inputs the upstream is missing, calls Execute,
// then downloads every output blob into the local CAS, so that later action
// cache hits served by execution.Service need no upstream contact.
package upstreamexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	errdetailpb "google.golang.org/genproto/googleapis/rpc/errdetails"
	rpcstatuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/model"
)

// defaultMaxBatchSize caps the total payload of a BatchUpdateBlobs or
// BatchReadBlobs request. It applies when GetCapabilities is unimplemented,
// omits a limit, or reports a larger one; 3 MiB leaves headroom for proto
// framing under the default 4 MiB gRPC message limit of the upstream server.
const defaultMaxBatchSize = 3 * 1024 * 1024

// Executor implements execution.ExecutorInterface by forwarding Execute to
// an upstream REAPI service. Inputs are uploaded from the local CAS to the
// upstream CAS as needed; outputs are downloaded back into the local CAS
// before the result is returned to the caller. It is safe for concurrent use.
type Executor struct {
	cas          *blobstore.ContentAddressableStorage
	instanceName string

	casClient  repb.ContentAddressableStorageClient
	execClient repb.ExecutionClient
	bsClient   bspb.ByteStreamClient

	// maxBatchSize is the smaller of the upstream's advertised batch limit
	// and defaultMaxBatchSize.
	maxBatchSize int64

	// fns lists the negotiated functions, which are the locally configured
	// digest functions that the upstream supports for both its cache and
	// its execution service. Execute rejects actions outside this set.
	fns []digest.Function

	// knownPresent is the presence cache. It maps an fnDigest to true once
	// its presence in the upstream CAS is confirmed (reported present or
	// uploaded by Execute), or to a *uploadOp while one goroutine is
	// establishing presence. Missing entries are unknown. Skipping
	// known-present digests avoids most FindMissingBlobs traffic. Entries go
	// stale when the upstream drops blobs (e.g. TTL eviction). Once the
	// upstream reports blobs of an action missing, Execute removes the
	// confirmed entries of all of the action's inputs and checks them again,
	// leaving in-flight *uploadOp entries for their owners to settle.
	knownPresent sync.Map
}

// New constructs an Executor over an existing gRPC connection. The connection
// is owned by the caller and is not closed by the Executor.
//
// instanceName is the REAPI instance name to forward (e.g.
// "projects/foo/instances/default_instance"). It may be empty for backends
// that don't use instance names (some local REAPI servers).
//
// localFns is the digest function set this server advertises to its own
// clients; empty defaults to SHA-256 only. From it, New negotiates the
// digest functions to execute under, which are the locally configured
// functions that the upstream supports for both cache and execution (see
// DigestFunctions).
//
// New calls GetCapabilities on the upstream and fails if the call fails,
// unless the upstream does not implement it, so that missing credentials or
// a wrong endpoint fail at startup rather than on every Execute.
func New(ctx context.Context, conn *grpc.ClientConn, cas *blobstore.ContentAddressableStorage, instanceName string, localFns []digest.Function) (*Executor, error) {
	// Validate the arguments.
	if conn == nil {
		return nil, errors.New("upstreamexec.New: conn must not be nil")
	}
	if cas == nil {
		return nil, errors.New("upstreamexec.New: cas must not be nil")
	}
	if len(localFns) == 0 {
		localFns = []digest.Function{digest.SHA256}
	}
	e := &Executor{
		cas:          cas,
		instanceName: instanceName,
		casClient:    repb.NewContentAddressableStorageClient(conn),
		execClient:   repb.NewExecutionClient(conn),
		bsClient:     bspb.NewByteStreamClient(conn),
		maxBatchSize: defaultMaxBatchSize,
	}

	// Probe the upstream's capabilities. Unimplemented is tolerated because
	// some minimal REAPI servers have no Capabilities service. For such an
	// upstream, New uses defaultMaxBatchSize, which is safe for it, and
	// negotiates as if it supported only SHA-256.
	caps, err := repb.NewCapabilitiesClient(conn).GetCapabilities(ctx, &repb.GetCapabilitiesRequest{
		InstanceName: instanceName,
	})
	switch {
	case err == nil:
		if limit := caps.GetCacheCapabilities().GetMaxBatchTotalSizeBytes(); limit > 0 {
			e.maxBatchSize = min(limit, defaultMaxBatchSize)
		}
	case status.Code(err) == codes.Unimplemented:
		slog.Warn("upstreamexec: upstream does not implement GetCapabilities; using default batch size", "error", err)
		caps = nil
	default:
		return nil, fmt.Errorf("upstreamexec.New: GetCapabilities probe failed: %w", err)
	}

	// Negotiate the digest functions.
	e.fns, err = negotiateDigestFunctions(localFns, caps)
	if err != nil {
		return nil, fmt.Errorf("upstreamexec.New: %w", err)
	}
	return e, nil
}

// DigestFunctions returns the negotiated digest functions in localFns order.
// The returned slice is shared and must not be modified.
func (e *Executor) DigestFunctions() []digest.Function {
	return e.fns
}

// negotiateDigestFunctions returns the locally configured digest functions
// that the upstream supports for both its cache and its execution service.
// The cache set matters because the proxy uploads inputs and downloads
// outputs, not only calls Execute. A nil caps or an absent/empty capability
// set conservatively counts as SHA-256 only. It fails if the upstream
// disables execution or the set is empty.
func negotiateDigestFunctions(localFns []digest.Function, caps *repb.ServerCapabilities) ([]digest.Function, error) {
	// Determine the upstream's cache set.
	cacheFns := []repb.DigestFunction_Value{repb.DigestFunction_SHA256}
	if cc := caps.GetCacheCapabilities(); len(cc.GetDigestFunctions()) > 0 {
		cacheFns = cc.GetDigestFunctions()
	}

	// Determine the upstream's execution set.
	execFns := []repb.DigestFunction_Value{repb.DigestFunction_SHA256}
	if ec := caps.GetExecutionCapabilities(); ec != nil {
		if !ec.GetExecEnabled() {
			return nil, errors.New("upstream advertises exec_enabled=false; it does not accept Execute requests")
		}
		switch {
		case len(ec.GetDigestFunctions()) > 0:
			execFns = ec.GetDigestFunctions()
		case ec.GetDigestFunction() != repb.DigestFunction_UNKNOWN:
			execFns = []repb.DigestFunction_Value{ec.GetDigestFunction()}
		}
	}

	// Intersect both with the local set.
	var out []digest.Function
	for _, fn := range localFns {
		if slices.Contains(cacheFns, fn.Value()) && slices.Contains(execFns, fn.Value()) {
			out = append(out, fn)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no digest function is supported by both this server and the upstream: local %s, upstream cache %v, upstream execution %v", fnNames(localFns), cacheFns, execFns)
	}
	return out, nil
}

// fnNames formats a digest function set for error messages.
func fnNames(fns []digest.Function) string {
	names := make([]string, len(fns))
	for i, fn := range fns {
		names[i] = fn.String()
	}
	return strings.Join(names, ",")
}

// MaxParallelism returns 0, which means no limit: actions run on the
// upstream, which schedules them on its own workers.
func (e *Executor) MaxParallelism() int { return 0 }

// Execute implements execution.ExecutorInterface. It uploads any blobs the
// upstream is missing, calls Execute upstream, and pulls every output blob
// into the local CAS before returning.
func (e *Executor) Execute(action *model.Action) (*repb.ActionResult, error) {
	// Reject functions outside the negotiated set up front instead of
	// failing deep inside the upload phase.
	if !slices.Contains(e.fns, action.Fn) {
		return nil, status.Errorf(codes.InvalidArgument, "upstream proxy supports digest functions %s, got %s", fnNames(e.fns), action.Fn)
	}

	// Run the upstream calls under a background context, because a client
	// hangup must not cancel an execution that merged requests share, or
	// whose result a retrying client can find in the local action cache.
	ctx := context.Background()

	// Ensure the upstream has every blob this action needs.
	inputs := collectInputDigests(action)
	if err := e.ensurePresent(ctx, action.Fn, inputs); err != nil {
		return nil, err
	}

	// Forward Execute and wait for the final ExecuteResponse. If the upstream
	// reports blobs missing, typically because it evicted a blob whose
	// presence the proxy memoized, restore the inputs from the local CAS and
	// retry once. Any other failure is returned.
	ar, err := e.executeAndWait(ctx, action.Fn, action.ActionDigest.Proto(), action.SkipCacheLookup)
	if mbErr, ok := errors.AsType[*upstreamMissingBlobsError](err); ok {
		ar, err = e.recoverMissingAndRetry(ctx, action, inputs, mbErr)
	}
	if err != nil {
		return nil, err
	}

	// Pull every blob the result references into the local CAS so future
	// calls can hit the local action cache without the upstream.
	if err := e.downloadOutputs(ctx, action.Fn, ar); err != nil {
		return nil, fmt.Errorf("download outputs: %w", err)
	}
	return ar, nil
}

// ensurePresent uploads every blob in digests that the upstream CAS lacks,
// trusting the presence cache for the digests it confirms. Each digest falls
// into one of three buckets:
//   - confirmed present by the presence cache: nothing to do.
//   - claimed by another goroutine: wait for its check or upload.
//   - claimed by this call: FindMissingBlobs, then upload as needed.
//
// Concurrent calls with overlapping digests therefore don't duplicate
// FindMissingBlobs RPCs or uploads.
func (e *Executor) ensurePresent(ctx context.Context, fn digest.Function, digests []digest.Digest) error {
	// Claim the digests. Claims still in newBlobs at return were never
	// resolved, typically because an RPC failed; settle them as failed so
	// that waiters don't block.
	newBlobs, pendingBlobs := e.claimDigests(fn, digests)
	defer e.settleAll(fn, newBlobs, errUploadAborted)

	// Check and upload the claimed digests, then wait for the others.
	if err := e.uploadClaimed(ctx, fn, newBlobs); err != nil {
		return err
	}
	return e.waitPending(ctx, pendingBlobs)
}

// uploadClaimed runs FindMissingBlobs over every digest the caller owns
// (newBlobs), uploads the ones the upstream reports as missing, and settles
// the corresponding uploadOps with success, removing them from newBlobs.
// Digests that turn out to be present upstream are settled without an
// upload, so waiters in other goroutines can proceed.
//
// On error, the claims not yet settled stay in newBlobs, and the caller must
// settle them as failed so that waiters see an error instead of blocking.
func (e *Executor) uploadClaimed(ctx context.Context, fn digest.Function, newBlobs map[digest.Digest]*uploadOp) error {
	if len(newBlobs) == 0 {
		return nil
	}

	// Ask the upstream which claimed digests it is missing.
	toCheck := slices.Collect(maps.Keys(newBlobs))
	missing, err := e.findMissing(ctx, fn, toCheck)
	if err != nil {
		return fmt.Errorf("find missing inputs: %w", err)
	}

	// Settle the digests the upstream already has.
	missingSet := make(map[digest.Digest]struct{}, len(missing))
	for _, d := range missing {
		missingSet[d] = struct{}{}
	}
	for d, uop := range newBlobs {
		if _, m := missingSet[d]; !m {
			e.settleClaim(fn, d, uop, nil)
			delete(newBlobs, d)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	// Upload the missing digests and settle their claims.
	slog.Info("upstreamexec: uploading missing inputs",
		"count", len(missing), "claimed", len(toCheck))
	if err := e.upload(ctx, fn, missing); err != nil {
		return fmt.Errorf("upload missing inputs: %w", err)
	}
	e.settleAll(fn, newBlobs, nil)
	return nil
}

// executeAndWait submits an Execute request and reads the operation stream
// until the final Done operation arrives, then unwraps the ExecuteResponse.
// Missing blobs yield *upstreamMissingBlobsError, whether the upstream reports
// them in the ExecuteResponse or as the status of the call.
//
// If the stream to the upstream breaks mid-action, the error is returned and
// the client retries the whole Execute; the upstream operation is not resumed
// with WaitExecution.
func (e *Executor) executeAndWait(ctx context.Context, fn digest.Function, actionDigest *repb.Digest, skipCacheLookup bool) (*repb.ActionResult, error) {
	// Open the stream under a cancelable context, so that drainStream can
	// unblock a server that never closes it and the deferred cancel tears it
	// down once the result is in hand.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := e.execClient.Execute(streamCtx, &repb.ExecuteRequest{
		InstanceName:    e.instanceName,
		ActionDigest:    actionDigest,
		SkipCacheLookup: skipCacheLookup,
		DigestFunction:  fn.Value(),
	})
	if err != nil {
		return nil, executeCallError(fn, "Execute", err)
	}

	// Read operations until the final one.
	for {
		op, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("Execute stream ended without a Done operation")
		}
		if err != nil {
			return nil, executeCallError(fn, "Execute recv", err)
		}
		if !op.GetDone() {
			// Non-final operations only carry progress metadata; there is
			// nothing to act on.
			continue
		}

		// Drain the stream so that gRPC sees the server's trailers and EOF
		// and reports a clean close instead of Canceled when the deferred
		// cancel fires. Any operation after Done violates the protocol, but
		// the valid final operation is already in hand, so only warn.
		extra := 0
		clean := drainStream(defaultStreamDrainTimeout, cancel, func() {
			for {
				if _, err := stream.Recv(); err != nil {
					return
				}
				extra++
			}
		})
		if !clean {
			slog.Warn("upstreamexec: stream drain after Execute Done timed out",
				"name", op.GetName(), "timeout", defaultStreamDrainTimeout)
		}
		if extra > 0 {
			slog.Warn("upstreamexec: server sent extra operations after Done",
				"name", op.GetName(), "extra", extra)
		}
		return unwrapExecuteResponse(fn, op)
	}
}

// executeCallError converts an error of the upstream Execute call into the
// error executeAndWait returns: *upstreamMissingBlobsError if its status
// reports missing blobs under fn, and otherwise err wrapped with what.
func executeCallError(fn digest.Function, what string, err error) error {
	if st, ok := status.FromError(err); ok {
		if mb := parseMissingBlobsStatus(fn, st.Proto()); mb != nil {
			return mb
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// unwrapExecuteResponse extracts the ActionResult from a Done Operation. An
// action that ran and exited non-zero is a successful result. A failure in
// Operation.error yields that status as an error. A failed
// ExecuteResponse.status yields *upstreamMissingBlobsError if it reports
// missing blobs under fn, and otherwise a gRPC status error with the
// upstream's code.
func unwrapExecuteResponse(fn digest.Function, op *longrunningpb.Operation) (*repb.ActionResult, error) {
	// Reject a failed operation. REAPI forbids an upstream to set
	// Operation.error, but keep the code of one that does.
	if errStatus := op.GetError(); errStatus != nil {
		return nil, status.ErrorProto(errStatus)
	}

	// Decode the ExecuteResponse.
	respAny := op.GetResponse()
	if respAny == nil {
		return nil, errors.New("upstream Execute Done operation had no response")
	}
	var resp repb.ExecuteResponse
	if err := proto.Unmarshal(respAny.Value, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal ExecuteResponse: %w", err)
	}

	// Map a failed status. Missing upstream blobs get their own error type
	// so that Execute can re-upload them; it must not be a
	// *blobstore.MissingBlobsError, which reports blobs missing from the
	// local CAS to the client.
	if resp.Status != nil && codes.Code(resp.Status.Code) != codes.OK {
		if mb := parseMissingBlobsStatus(fn, resp.Status); mb != nil {
			return nil, mb
		}
		return nil, status.Errorf(codes.Code(resp.Status.Code),
			"upstream Execute failed: %s", resp.Status.Message)
	}
	if resp.Result == nil {
		return nil, errors.New("upstream ExecuteResponse missing Result")
	}
	return resp.Result, nil
}

// upstreamMissingBlobsError is returned when the upstream rejected Execute
// because it doesn't have one or more referenced blobs. Unlike
// blobstore.MissingBlobsError, which reports blobs missing from the local
// CAS and becomes FAILED_PRECONDITION for the client, it never leaves the
// package. Execute restores the action's inputs from the local CAS and
// retries once, and recoverMissingAndRetry turns a second report into an
// Internal error.
type upstreamMissingBlobsError struct {
	blobs []digest.Digest
	msg   string
}

// Error implements error.
func (e *upstreamMissingBlobsError) Error() string {
	return fmt.Sprintf("upstream missing %d blob(s): %s", len(e.blobs), e.msg)
}

// parseMissingBlobsStatus extracts digests from a FAILED_PRECONDITION
// status whose details carry one or more PreconditionFailure entries with
// MISSING violations. It returns nil if the status doesn't match this shape.
func parseMissingBlobsStatus(fn digest.Function, s *rpcstatuspb.Status) *upstreamMissingBlobsError {
	if codes.Code(s.GetCode()) != codes.FailedPrecondition {
		return nil
	}
	var blobs []digest.Digest
	for _, det := range s.GetDetails() {
		var pf errdetailpb.PreconditionFailure
		if err := det.UnmarshalTo(&pf); err != nil {
			continue
		}
		for _, v := range pf.GetViolations() {
			if v.GetType() != "MISSING" {
				continue
			}
			d, ok := parseBlobSubject(fn, v.GetSubject())
			if !ok {
				continue
			}
			blobs = append(blobs, d)
		}
	}
	if len(blobs) == 0 {
		return nil
	}
	return &upstreamMissingBlobsError{blobs: blobs, msg: s.GetMessage()}
}

// parseBlobSubject parses a "blobs/{digest_function/}{hash}/{size}"
// PreconditionFailure Subject into a digest under fn. The digest-function
// segment is optional even for the non-legacy functions (servers differ);
// when present it must name fn. It returns ok=false if the format doesn't
// match.
func parseBlobSubject(fn digest.Function, subject string) (digest.Digest, bool) {
	parts := strings.Split(subject, "/")
	if len(parts) == 4 && parts[1] == fn.String() {
		parts = append(parts[:1], parts[2:]...)
	}
	if len(parts) != 3 || parts[0] != "blobs" {
		return digest.Digest{}, false
	}
	size, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return digest.Digest{}, false
	}
	d, err := fn.Validate(parts[1], size)
	if err != nil {
		return digest.Digest{}, false
	}
	return d, true
}

// recoverMissingAndRetry handles an upstream report of missing blobs by
// restoring the action's inputs, as collectInputDigests returns them, in the
// upstream CAS and retrying Execute once. A report may name only some of the
// missing blobs: Kajiya, for example, reports a missing Action or Command
// alone, and leaves out what lies below a missing directory. So recovery drops
// what the presence cache confirms about every input, checks them all with
// FindMissingBlobs, and uploads the missing ones.
//
// If the local CAS lacks a reported blob too, it fails without retrying,
// because neither a retry nor passing the condition through would help. If
// the retry reports missing blobs again, it fails with codes.Internal. The
// upstream has just reported every input present or accepted its upload, so
// passing FAILED_PRECONDITION through would make the client re-upload blobs
// that the local CAS already has.
//
// The recheck goes through the same claim protocol as Execute's upload phase,
// so concurrent Execute calls recovering from the same eviction share each
// check and upload.
func (e *Executor) recoverMissingAndRetry(ctx context.Context, action *model.Action, inputs []digest.Digest, mbErr *upstreamMissingBlobsError) (*repb.ActionResult, error) {
	// Check that the local CAS has every reported blob.
	var stillMissing []digest.Digest
	for _, d := range mbErr.blobs {
		if !e.cas.Has(action.Fn, d) {
			stillMissing = append(stillMissing, d)
		}
	}
	if len(stillMissing) > 0 {
		return nil, fmt.Errorf("upstream Execute missing %d blob(s) that the local CAS lacks too: %v",
			len(stillMissing), stillMissing)
	}
	slog.Warn("upstreamexec: upstream reported blobs missing; restoring the inputs",
		"reported", len(mbErr.blobs), "inputs", len(inputs), "action", action.ActionDigest)

	// Forget the confirmed presence of the inputs and establish it again.
	// Only a confirmed entry is removed: an in-flight *uploadOp belongs to
	// another Execute that is establishing presence right now, and removing
	// it would let this call upload the same blob a second time. If a
	// concurrent Execute recovers too, only one of them wins each digest's
	// LoadOrStore and the others wait on the winner's uploadOp.
	for _, d := range inputs {
		e.knownPresent.CompareAndDelete(fnDigest{action.Fn, d}, true)
	}
	if err := e.ensurePresent(ctx, action.Fn, inputs); err != nil {
		return nil, fmt.Errorf("restore inputs after upstream missing-blobs: %w", err)
	}

	// Retry Execute.
	ar, err := e.executeAndWait(ctx, action.Fn, action.ActionDigest.Proto(), action.SkipCacheLookup)
	if mbErr, ok := errors.AsType[*upstreamMissingBlobsError](err); ok {
		return nil, status.Errorf(codes.Internal, "upstream still reports blobs missing after the proxy restored the inputs: %v", mbErr)
	}
	return ar, err
}
