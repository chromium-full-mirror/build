// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package upstreamexec

import (
	"context"
	"errors"
	"fmt"

	"go.chromium.org/build/hashigo/digest"
)

// uploadOp coordinates a single in-flight upload across goroutines. The
// goroutine that wins the LoadOrStore race on knownPresent owns the upload
// and must call settleClaim when it finishes; other goroutines that find an
// existing uploadOp wait on done and read err once it's closed.
type uploadOp struct {
	done chan struct{}
	err  error // safe to read once done is closed
}

// newUploadOp returns an unsettled uploadOp.
func newUploadOp() *uploadOp {
	return &uploadOp{done: make(chan struct{})}
}

// errUploadAborted is the error settleClaim records when the owning Execute
// call returns without explicitly resolving a claim, typically because an
// earlier RPC failed.
var errUploadAborted = errors.New("upload aborted before completion")

// fnDigest keys the presence cache. Upstream blob presence is per digest
// function (the upstream CAS namespaces blobs by function), so the digest
// value alone would be ambiguous.
type fnDigest struct {
	fn digest.Function
	d  digest.Digest
}

// claimDigests looks up each of inputs in the presence cache. It returns in
// newBlobs the digests that this call claims, and the caller must settle each
// of them with settleClaim before returning, on success and on failure. It
// returns in pendingBlobs the digests that another call has claimed and is
// still checking or uploading, which the caller can wait for with
// waitPending. It skips the digests that are confirmed present.
func (e *Executor) claimDigests(fn digest.Function, inputs []digest.Digest) (newBlobs, pendingBlobs map[digest.Digest]*uploadOp) {
	newBlobs = make(map[digest.Digest]*uploadOp)
	pendingBlobs = make(map[digest.Digest]*uploadOp)
	for _, d := range inputs {
		v, loaded := e.knownPresent.LoadOrStore(fnDigest{fn, d}, newUploadOp())
		if !loaded {
			newBlobs[d] = v.(*uploadOp)
			continue
		}
		switch x := v.(type) {
		case bool:
			// Already confirmed present: nothing to do.
		case *uploadOp:
			pendingBlobs[d] = x
		}
	}
	return newBlobs, pendingBlobs
}

// settleClaim records the outcome of a claim the caller owns. On success, it
// replaces the *uploadOp entry in the presence cache with true. On failure,
// it removes the entry so that a later Execute call checks the digest again.
// Either way, it closes the uop's done channel so that any waiters unblock.
func (e *Executor) settleClaim(fn digest.Function, d digest.Digest, uop *uploadOp, err error) {
	// The CompareAnd* calls are defensive, because only the owner settles a
	// claim and recovery removes only confirmed entries, so the entry is
	// still uop.
	uop.err = err
	if err == nil {
		e.knownPresent.CompareAndSwap(fnDigest{fn, d}, uop, true)
	} else {
		e.knownPresent.CompareAndDelete(fnDigest{fn, d}, uop)
	}
	close(uop.done)
}

// settleAll calls settleClaim with err on every entry of m and removes the
// entries from m, so a deferred settleAll after explicit settlement is a
// no-op.
func (e *Executor) settleAll(fn digest.Function, m map[digest.Digest]*uploadOp, err error) {
	for d, uop := range m {
		e.settleClaim(fn, d, uop, err)
		delete(m, d)
	}
}

// waitPending blocks until each uop in pending is resolved, and returns the
// first error any of them surfaced. It returns early if ctx is canceled.
func (e *Executor) waitPending(ctx context.Context, pending map[digest.Digest]*uploadOp) error {
	for d, uop := range pending {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-uop.done:
			if uop.err != nil {
				return fmt.Errorf("upload of %s by another goroutine: %w", d, uop.err)
			}
		}
	}
	return nil
}
