// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package reapi_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/stats"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/o11y/trace"
	"go.chromium.org/build/siso/reapi"
	"go.chromium.org/build/siso/reapi/reapitest"
)

// A blocked wait in these tests is a test failure that surfaces as a
// hang; it is bounded by `go test -timeout`.

// TestUploadAll_RequesterCancelDoesNotCancelUpload checks that canceling
// the requester's context makes UploadAll return promptly while the
// upload operation itself keeps running to completion, so that a later
// request for the same blob is served by that operation. b/565765743
func TestUploadAll_RequesterCancelDoesNotCancelUpload(t *testing.T) {
	ctx := t.Context()
	gate := reapitest.NewRPCGate(rpb.ContentAddressableStorage_BatchUpdateBlobs_FullMethodName)
	t.Cleanup(gate.Release)
	cl := reapitest.NewWithOption(ctx, t, &reapitest.Fake{}, reapi.Option{}, gate.DialOption())

	ds := blob.NewStore()
	d := blob.FromBytes(digest.SHA256, "blob", []byte("detached upload"))
	ds.Set(d)

	// Requester 1: canceled while its upload is held in the gate.
	reqCtx, reqCancel := context.WithCancelCause(ctx)
	errCh := make(chan error, 1)
	go func() {
		_, err := cl.UploadAll(reqCtx, ds)
		errCh <- err
	}()
	gate.WaitEntered(ctx, t)
	wantCause := errors.New("local racer won")
	reqCancel(wantCause)
	if err := <-errCh; !errors.Is(err, wantCause) {
		t.Errorf("UploadAll(canceled)=%v; want cause %v", err, wantCause)
	}

	// Requester 2 for the same blob: waits on the pending upload
	// operation, which must still complete once the gate opens.
	errCh2 := make(chan error, 1)
	var n2 int
	go func() {
		var err error
		n2, err = cl.UploadAll(ctx, ds)
		errCh2 <- err
	}()
	gate.Release()
	if err := <-errCh2; err != nil {
		t.Fatalf("UploadAll(pending)=%d,%v; want _,nil (requester 1's cancel must not poison the shared upload)", n2, err)
	}
	// The blob was uploaded by requester 1's operation, not this one.
	if n2 != 0 {
		t.Errorf("UploadAll(pending) uploaded %d; want 0", n2)
	}

	// And the blob is actually in the CAS.
	got, err := cl.Get(ctx, d.Digest(), "blob")
	if err != nil || string(got) != "detached upload" {
		t.Errorf("Get=%q,%v; want %q,nil", got, err, "detached upload")
	}
}

// TestUploadAll_CloseCancelsUpload checks that Close cancels in-flight
// upload operations and releases their waiters.
func TestUploadAll_CloseCancelsUpload(t *testing.T) {
	ctx := t.Context()
	gate := reapitest.NewRPCGate(rpb.ContentAddressableStorage_BatchUpdateBlobs_FullMethodName)
	t.Cleanup(gate.Release)
	cl := reapitest.NewWithOption(ctx, t, &reapitest.Fake{}, reapi.Option{}, gate.DialOption())

	ds := blob.NewStore()
	ds.Set(blob.FromBytes(digest.SHA256, "blob", []byte("canceled by close")))

	errCh := make(chan error, 1)
	go func() {
		_, err := cl.UploadAll(ctx, ds)
		errCh <- err
	}()
	gate.WaitEntered(ctx, t)

	closed := make(chan error, 1)
	go func() { closed <- cl.Close() }()
	if err := <-errCh; !errors.Is(err, reapi.ErrClientClosed) {
		t.Errorf("UploadAll=%v after Close; want %v", err, reapi.ErrClientClosed)
	}
	<-closed
}

// TestUploadAll_QueuedRequestsCompleteAndCloseFailsThem checks the worker
// pool: with a single worker held in the gate, further requests queue up
// and are served in turn once the worker is free; requests still queued
// when the client is closed fail with the close cause instead of leaving
// their waiters hanging.
func TestUploadAll_QueuedRequestsCompleteAndCloseFailsThem(t *testing.T) {
	ctx := t.Context()
	gate := reapitest.NewRPCGate(rpb.ContentAddressableStorage_BatchUpdateBlobs_FullMethodName)
	t.Cleanup(gate.Release)
	cl := reapitest.NewWithOption(ctx, t, &reapitest.Fake{}, reapi.Option{
		UploadLimit: 1,
	}, gate.DialOption())

	newStore := func(name string) *blob.Store {
		ds := blob.NewStore()
		ds.Set(blob.FromBytes(digest.SHA256, name, []byte("content of "+name)))
		return ds
	}
	type result struct {
		n   int
		err error
	}
	upload := func(cl *reapi.Client, ds *blob.Store) <-chan result {
		ch := make(chan result, 1)
		go func() {
			n, err := cl.UploadAll(ctx, ds)
			ch <- result{n, err}
		}()
		return ch
	}

	// Request 1 occupies the only worker; request 2 queues behind it.
	r1 := upload(cl, newStore("one"))
	gate.WaitEntered(ctx, t)
	r2 := upload(cl, newStore("two"))

	// Let the worker through: both requests complete, in order.
	gate.Release()
	for i, ch := range []<-chan result{r1, r2} {
		if r := <-ch; r.err != nil || r.n != 1 {
			t.Errorf("request %d: UploadAll=%d,%v; want 1,nil", i+1, r.n, r.err)
		}
	}

	// Hold the worker again, queue a request behind it, then close:
	// the held request fails (canceled RPC) and the queued one fails
	// with the close cause; neither waiter is left hanging.
	gate2 := reapitest.NewRPCGate(rpb.ContentAddressableStorage_FindMissingBlobs_FullMethodName)
	t.Cleanup(gate2.Release)
	cl2 := reapitest.NewWithOption(ctx, t, &reapitest.Fake{}, reapi.Option{
		UploadLimit: 1,
	}, gate2.DialOption())
	held := upload(cl2, newStore("held"))
	gate2.WaitEntered(ctx, t)
	queued := upload(cl2, newStore("queued"))
	closed := make(chan error, 1)
	go func() { closed <- cl2.Close() }()
	for name, ch := range map[string]<-chan result{"held": held, "queued": queued} {
		if r := <-ch; !errors.Is(r.err, reapi.ErrClientClosed) {
			t.Errorf("%s request: UploadAll=%d,%v after Close; want %v", name, r.n, r.err, reapi.ErrClientClosed)
		}
	}
	<-closed
}

type outPayloadHandler struct {
	method  string
	onSent  func()
	release chan struct{}
	once    sync.Once
}

type rpcMethodKey struct{}

func (h *outPayloadHandler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	return context.WithValue(ctx, rpcMethodKey{}, info.FullMethodName)
}

func (h *outPayloadHandler) HandleRPC(ctx context.Context, rs stats.RPCStats) {
	if _, ok := rs.(*stats.OutPayload); ok {
		if m, ok := ctx.Value(rpcMethodKey{}).(string); ok && m == h.method {
			h.once.Do(func() {
				h.onSent()
				<-h.release
			})
		}
	}
}

func (h *outPayloadHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

func (h *outPayloadHandler) HandleConn(context.Context, stats.ConnStats) {}

// TestUploadAll_ServerShutdownWaitsForInFlightHandler checks that shutting
// down a reapitest server while a detached BatchUpdateBlobs RPC is still
// writing blobs into its CAS dir waits for the handler to finish before
// t.TempDir's RemoveAll cleanup runs. b/575196094
func TestUploadAll_ServerShutdownWaitsForInFlightHandler(t *testing.T) {
	ctx := t.Context()
	reqCtx, reqCancel := context.WithCancel(ctx)
	defer reqCancel()
	sh := &outPayloadHandler{
		method:  rpb.ContentAddressableStorage_BatchUpdateBlobs_FullMethodName,
		onSent:  reqCancel,
		release: make(chan struct{}),
	}
	defer close(sh.release)
	cl := reapitest.NewWithOption(ctx, t, &reapitest.Fake{}, reapi.Option{}, grpc.WithStatsHandler(sh))
	t.Cleanup(func() {
		if err := cl.Close(); err != nil {
			t.Error(err)
		}
	})

	ds := blob.NewStore()
	for i := range 200 {
		name := fmt.Sprintf("blob-%d", i)
		ds.Set(blob.FromBytes(digest.SHA256, name, []byte(name)))
	}

	// Cancel reqCtx and hold the client goroutine inside OutPayload (after
	// the request frame has been queued to the transport) until UploadAll
	// observes the cancellation, so UploadAll deterministically returns
	// context.Canceled while the server handler is writing blobs.
	if _, err := cl.UploadAll(reqCtx, ds); !errors.Is(err, context.Canceled) {
		t.Errorf("UploadAll(canceled)=%v; want %v", err, context.Canceled)
	}
}

const byteStreamWriteMethod = "/google.bytestream.ByteStream/Write"

type observedUploadCall struct {
	rmdCount int
	rmd      *rpb.RequestMetadata
	spanCtx  oteltrace.SpanContext
}

type uploadCallRecorder struct {
	mu    sync.Mutex
	calls map[string][]observedUploadCall
}

// requestMetadata returns the RequestMetadata that md carries, or nil.
func requestMetadata(md metadata.MD) *rpb.RequestMetadata {
	v := md.Get(reapi.RequestMetadataKey)
	if len(v) == 0 {
		return nil
	}
	rmd := &rpb.RequestMetadata{}
	if err := proto.Unmarshal([]byte(v[0]), rmd); err != nil {
		return nil
	}
	return rmd
}

func (r *uploadCallRecorder) record(ctx context.Context, method string) {
	md, _ := metadata.FromOutgoingContext(ctx)
	rmd := requestMetadata(md)
	r.mu.Lock()
	r.calls[method] = append(r.calls[method], observedUploadCall{
		rmdCount: len(md.Get(reapi.RequestMetadataKey)),
		rmd:      rmd,
		spanCtx:  oteltrace.SpanContextFromContext(ctx),
	})
	r.mu.Unlock()
}

func (r *uploadCallRecorder) forMethod(method string) []observedUploadCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]observedUploadCall(nil), r.calls[method]...)
}

func newRecordingUploadClient(ctx context.Context, t *testing.T) (*reapi.Client, *uploadCallRecorder) {
	t.Helper()
	rec := &uploadCallRecorder{
		calls: make(map[string][]observedUploadCall),
	}
	unary := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		rec.record(ctx, method)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
	stream := func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		rec.record(ctx, method)
		return streamer(ctx, desc, cc, method, opts...)
	}
	cl := reapitest.NewWithOption(ctx, t, &reapitest.Fake{}, reapi.Option{},
		grpc.WithChainUnaryInterceptor(unary),
		grpc.WithChainStreamInterceptor(stream),
	)
	t.Cleanup(func() {
		if err := cl.Close(); err != nil {
			t.Error(err)
		}
	})
	return cl, rec
}

// TestUploadAll_PropagatesRequestMetadataAndTraceContext checks that detached
// CAS uploads carry the submitting requester's REAPI RequestMetadata (including
// per-action fields) and OpenTelemetry trace context on FindMissingBlobs,
// BatchUpdateBlobs, and ByteStream.Write RPCs, without duplicating
// requestmetadata-bin when the client's lifetime context already has one.
// b/587969386
func TestUploadAll_PropagatesRequestMetadataAndTraceContext(t *testing.T) {
	// Simulate siso ninja creating the client with base RequestMetadata on
	// the client's lifetime context.
	clientCtx := reapi.NewContext(t.Context(), &rpb.RequestMetadata{
		ToolInvocationId: "client-invocation",
	})
	cl, rec := newRecordingUploadClient(clientCtx, t)

	tc := trace.New(t.Context(), "01234567-89ab-cdef-0123-456789abcdef")
	reqCtx := trace.NewContext(t.Context(), tc)
	reqCtx, span := trace.NewSpan(reqCtx, "run-step")
	defer span.Close(nil)
	wantTraceID, wantSpanID, ok := span.RawIDs()
	if !ok {
		t.Fatal("span.RawIDs() returned ok=false")
	}
	wantRMD := &rpb.RequestMetadata{
		ToolInvocationId:        "step-invocation",
		CorrelatedInvocationsId: "job-1",
		ActionId:                "action-1",
		ActionMnemonic:          "cxx",
		TargetId:                "obj/foo.o",
	}
	reqCtx = reapi.NewContext(reqCtx, wantRMD)

	ds := blob.NewStore()
	ds.Set(blob.FromBytes(digest.SHA256, "small", []byte("small blob")))
	ds.Set(blob.FromBytes(digest.SHA256, "large", make([]byte, 4*1024*1024)))
	n, err := cl.UploadAll(reqCtx, ds)
	if err != nil || n != 2 {
		t.Fatalf("UploadAll()=%d,%v; want 2,nil", n, err)
	}

	wantMethods := []string{
		rpb.ContentAddressableStorage_FindMissingBlobs_FullMethodName,
		rpb.ContentAddressableStorage_BatchUpdateBlobs_FullMethodName,
		byteStreamWriteMethod,
	}
	for _, m := range wantMethods {
		got := rec.forMethod(m)
		if len(got) == 0 {
			t.Errorf("method %s: no calls recorded", m)
			continue
		}
		for i, c := range got {
			if c.rmdCount != 1 {
				t.Errorf("%s[%d]: %s entries = %d, want 1", m, i, reapi.RequestMetadataKey, c.rmdCount)
			}
			if c.rmd.GetToolInvocationId() != wantRMD.GetToolInvocationId() ||
				c.rmd.GetCorrelatedInvocationsId() != wantRMD.GetCorrelatedInvocationsId() ||
				c.rmd.GetActionId() != wantRMD.GetActionId() ||
				c.rmd.GetActionMnemonic() != wantRMD.GetActionMnemonic() ||
				c.rmd.GetTargetId() != wantRMD.GetTargetId() {
				t.Errorf("%s[%d]: RequestMetadata = %v, want %v", m, i, c.rmd, wantRMD)
			}
			if !c.spanCtx.IsValid() || c.spanCtx.TraceID() != wantTraceID || c.spanCtx.SpanID() != wantSpanID || !c.spanCtx.IsSampled() {
				t.Errorf("%s[%d]: SpanContext = (valid=%v trace=%s span=%s sampled=%v), want (true %s %s true)",
					m, i, c.spanCtx.IsValid(), c.spanCtx.TraceID(), c.spanCtx.SpanID(), c.spanCtx.IsSampled(),
					oteltrace.TraceID(wantTraceID), oteltrace.SpanID(wantSpanID))
			}
		}
	}
}
