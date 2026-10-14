// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package upstreamexec

import (
	"context"
	"time"
)

// defaultStreamDrainTimeout bounds how long drainStream waits for a server's
// trailing EOF after the caller already has the result. The trailer has
// normally arrived by then, so this only matters for a misbehaving peer that
// stops sending without closing the stream.
const defaultStreamDrainTimeout = 500 * time.Millisecond

// drainStream runs drain in a goroutine and reports whether it returned within
// timeout. On timeout it calls cancel to unblock drain and waits for drain to
// return before reporting false, so the caller may release anything drain
// uses.
//
// Returning early from a server-streaming RPC and then canceling the stream
// context makes gRPC report the otherwise successful call as Canceled to the
// server; draining first lets the trailing metadata and EOF arrive cleanly.
//
// drain takes no context because it can only loop over stream.Recv, which is
// bound to the context that opened the stream. Canceling that context, which
// is what cancel must do, is the only way to unblock it.
func drainStream(timeout time.Duration, cancel context.CancelFunc, drain func()) bool {
	// Run drain and wait for it up to the timeout.
	done := make(chan struct{})
	go func() {
		drain()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
	}

	// Unblock drain and wait for it to return.
	cancel()
	<-done
	return false
}
