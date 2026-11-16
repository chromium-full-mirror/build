// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"context"
	"errors"
	"testing"

	"go.chromium.org/build/siso/resource"
)

// TestAcquireFlushStaticFallback covers the nil-gate path: acquireFlush
// must fall back to the static FlushSemaphore and expose the same
// release(error) shape as the adaptive gate. Releasing must return the
// slot so a subsequent acquire succeeds.
func TestAcquireFlushStaticFallback(t *testing.T) {
	ctx := t.Context()

	gotCtx, done, err := acquireFlush(ctx, nil)
	if err != nil {
		t.Fatalf("acquireFlush(nil gate): %v", err)
	}
	if gotCtx == nil {
		t.Fatal("acquireFlush returned nil ctx")
	}
	if done == nil {
		t.Fatal("acquireFlush returned nil release")
	}
	done(nil) // release the static semaphore slot; must not panic

	// The slot was returned, so another acquire/release round works.
	_, done2, err := acquireFlush(ctx, nil)
	if err != nil {
		t.Fatalf("second acquireFlush(nil gate): %v", err)
	}
	done2(nil)
}

// TestAcquireFlushCanceledContext verifies that neither gate admits a flush
// once its context is canceled.
func TestAcquireFlushCanceledContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate *resource.Network
	}{
		{"static", nil},
		{"network", resource.NewNetwork("test-flush", 1, 1, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			// The static semaphore picks at random between a free slot and
			// the canceled context, so try several times.
			for range 20 {
				_, done, err := acquireFlush(ctx, tc.gate)
				done(err)
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("acquireFlush(canceled ctx) error = %v, want %v", err, context.Canceled)
				}
			}
		})
	}
}
