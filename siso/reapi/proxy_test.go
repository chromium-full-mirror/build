// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package reapi

import (
	"context"
	"net"
	"testing"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// marshalRequestMetadata returns the wire value of the requestmetadata-bin
// header for rmd.
func marshalRequestMetadata(t *testing.T, rmd *rpb.RequestMetadata) string {
	t.Helper()
	b, err := proto.Marshal(rmd)
	if err != nil {
		t.Fatalf("marshal %v: %v", rmd, err)
	}
	return string(b)
}

// TestForwardMetadataStreamInterceptor verifies that a streaming RPC handler
// sees the client's RequestMetadata on its outgoing context, so calls the proxy
// makes upstream still carry the tool_invocation_id and target_id.
func TestForwardMetadataStreamInterceptor(t *testing.T) {
	want := &rpb.RequestMetadata{
		ToolInvocationId: "invocation-1",
		TargetId:         "//base:base",
		ActionMnemonic:   "cxx",
	}

	// The handler stands in for a proxy handler: it reports what it would send
	// upstream, i.e. the outgoing metadata of the context it was given.
	gotMD := make(chan metadata.MD, 1)
	handler := func(_ any, stream grpc.ServerStream) error {
		md, _ := metadata.FromOutgoingContext(stream.Context())
		gotMD <- md
		return nil
	}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(
		grpc.UnknownServiceHandler(handler),
		grpc.ChainStreamInterceptor(forwardMetadataStreamInterceptor),
	)
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer conn.Close()

	ctx := NewContext(t.Context(), proto.Clone(want).(*rpb.RequestMetadata))
	_ = conn.Invoke(ctx, "/probe.Service/Probe", &emptypb.Empty{}, &emptypb.Empty{})

	md := <-gotMD
	vals := md.Get(requestMetadataKey)
	if len(vals) != 1 {
		t.Fatalf("outgoing %s entries = %d, want 1", requestMetadataKey, len(vals))
	}
	got := &rpb.RequestMetadata{}
	if err := proto.Unmarshal([]byte(vals[0]), got); err != nil {
		t.Fatalf("unmarshal request metadata: %v", err)
	}
	if got.GetToolInvocationId() != want.GetToolInvocationId() {
		t.Errorf("tool_invocation_id = %q, want %q", got.GetToolInvocationId(), want.GetToolInvocationId())
	}
	if got.GetTargetId() != want.GetTargetId() {
		t.Errorf("target_id = %q, want %q", got.GetTargetId(), want.GetTargetId())
	}
	if got.GetActionMnemonic() != want.GetActionMnemonic() {
		t.Errorf("action_mnemonic = %q, want %q", got.GetActionMnemonic(), want.GetActionMnemonic())
	}
}

// TestForwardMetadataUnaryInterceptor verifies the unary interceptor hands the
// handler a context whose outgoing metadata carries the client's
// RequestMetadata.
func TestForwardMetadataUnaryInterceptor(t *testing.T) {
	want := &rpb.RequestMetadata{ToolInvocationId: "invocation-1"}
	in := metadata.Pairs(requestMetadataKey, marshalRequestMetadata(t, want))
	ctx := metadata.NewIncomingContext(t.Context(), in)

	var gotCtx context.Context
	handler := func(ctx context.Context, _ any) (any, error) {
		gotCtx = ctx
		return nil, nil
	}
	if _, err := forwardMetadataUnaryInterceptor(ctx, nil, nil, handler); err != nil {
		t.Fatalf("interceptor: %v", err)
	}

	md, ok := metadata.FromOutgoingContext(gotCtx)
	if !ok {
		t.Fatal("handler context has no outgoing metadata, want the forwarded request metadata")
	}
	vals := md.Get(requestMetadataKey)
	if len(vals) != 1 {
		t.Fatalf("outgoing %s entries = %d, want 1", requestMetadataKey, len(vals))
	}
	got := &rpb.RequestMetadata{}
	if err := proto.Unmarshal([]byte(vals[0]), got); err != nil {
		t.Fatalf("unmarshal request metadata: %v", err)
	}
	if got.GetToolInvocationId() != want.GetToolInvocationId() {
		t.Errorf("tool_invocation_id = %q, want %q", got.GetToolInvocationId(), want.GetToolInvocationId())
	}
}

// TestForwardMetadataUnaryInterceptorNoMetadata verifies a request without
// RequestMetadata passes through untouched.
func TestForwardMetadataUnaryInterceptorNoMetadata(t *testing.T) {
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("other-key", "v"))

	var gotCtx context.Context
	handler := func(ctx context.Context, _ any) (any, error) {
		gotCtx = ctx
		return nil, nil
	}
	if _, err := forwardMetadataUnaryInterceptor(ctx, nil, nil, handler); err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if md, ok := metadata.FromOutgoingContext(gotCtx); ok {
		t.Errorf("outgoing metadata = %v, want none", md)
	}
}

type fakeOperationsServer struct {
	longrunningpb.UnimplementedOperationsServer
	op *longrunningpb.Operation
}

func (f *fakeOperationsServer) GetOperation(_ context.Context, req *longrunningpb.GetOperationRequest) (*longrunningpb.Operation, error) {
	if req.GetName() == f.op.GetName() {
		return f.op, nil
	}
	return nil, status.Error(codes.NotFound, "operation not found")
}

func TestOperationsProxy_GetOperation(t *testing.T) {
	wantOp := &longrunningpb.Operation{
		Name: "operations/test-op-123",
		Done: true,
	}
	fake := &fakeOperationsServer{op: wantOp}

	backendLis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	longrunningpb.RegisterOperationsServer(srv, fake)
	go srv.Serve(backendLis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return backendLis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	opProxy := &operationsProxy{client: longrunningpb.NewOperationsClient(conn)}

	proxyLis := bufconn.Listen(1 << 20)
	proxySrv := grpc.NewServer()
	longrunningpb.RegisterOperationsServer(proxySrv, opProxy)
	go proxySrv.Serve(proxyLis)
	t.Cleanup(proxySrv.Stop)

	proxyConn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return proxyLis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("NewClient proxy: %v", err)
	}
	t.Cleanup(func() { proxyConn.Close() })

	cli := longrunningpb.NewOperationsClient(proxyConn)
	gotOp, err := cli.GetOperation(t.Context(), &longrunningpb.GetOperationRequest{Name: "operations/test-op-123"})
	if err != nil {
		t.Fatalf("GetOperation failed: %v", err)
	}
	if gotOp.GetName() != wantOp.GetName() || gotOp.GetDone() != wantOp.GetDone() {
		t.Errorf("GetOperation = %v, want %v", gotOp, wantOp)
	}
}
