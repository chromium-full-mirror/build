// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package firstbyte

import (
	"sync"
	"testing"

	"google.golang.org/grpc/stats"
)

// No-Signal ctx: TagRPC passthrough, HandleRPC no-op on every event.
func TestHandler_NoSignalInCtxIsNoOp(t *testing.T) {
	h := Handler
	ctx := h.TagRPC(t.Context(), &stats.RPCTagInfo{FullMethodName: "/x/y"})
	h.HandleRPC(ctx, &stats.Begin{})
	h.HandleRPC(ctx, &stats.OutHeader{})
	h.HandleRPC(ctx, &stats.InHeader{})
	h.HandleRPC(ctx, &stats.InPayload{})
	h.HandleRPC(ctx, &stats.End{})
}

func TestHandler_InPayloadClosesSignal(t *testing.T) {
	parent, sig := WithSignal(t.Context())
	h := Handler
	rpcCtx := h.TagRPC(parent, &stats.RPCTagInfo{FullMethodName: "/google.bytestream.ByteStream/Read"})
	select {
	case <-sig.Fired():
		t.Fatal("Signal fired before InPayload")
	default:
	}
	h.HandleRPC(rpcCtx, &stats.InPayload{})
	select {
	case <-sig.Fired():
	default:
		t.Fatal("Signal did not fire on InPayload")
	}
}

// Multiple InPayloads on a shared ctx must not double-close.
func TestHandler_InPayloadIdempotent(t *testing.T) {
	parent, _ := WithSignal(t.Context())
	h := Handler
	rpcCtx := h.TagRPC(parent, &stats.RPCTagInfo{FullMethodName: "/x/y"})
	h.HandleRPC(rpcCtx, &stats.InPayload{})
	h.HandleRPC(rpcCtx, &stats.InPayload{}) // must not panic
}

// Concurrent InPayloads on a shared Signal: exactly one close.
func TestHandler_ConcurrentInPayloadFiresOnce(t *testing.T) {
	parent, sig := WithSignal(t.Context())
	h := Handler
	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			rpcCtx := h.TagRPC(parent, &stats.RPCTagInfo{FullMethodName: "/x/y"})
			h.HandleRPC(rpcCtx, &stats.InPayload{})
		}()
	}
	wg.Wait()
	select {
	case <-sig.Fired():
	default:
		t.Fatal("Signal did not fire under concurrent InPayloads")
	}
}

// Non-InPayload events do not fire the signal; only InPayload does.
func TestHandler_OnlyInPayloadFires(t *testing.T) {
	parent, sig := WithSignal(t.Context())
	h := Handler
	rpcCtx := h.TagRPC(parent, &stats.RPCTagInfo{FullMethodName: "/x/y"})
	h.HandleRPC(rpcCtx, &stats.Begin{})
	h.HandleRPC(rpcCtx, &stats.OutHeader{})
	h.HandleRPC(rpcCtx, &stats.InHeader{})
	select {
	case <-sig.Fired():
		t.Fatal("Signal fired on non-InPayload events")
	default:
	}
	h.HandleRPC(rpcCtx, &stats.End{})
	select {
	case <-sig.Fired():
		t.Fatal("Signal fired on End event")
	default:
	}
}
