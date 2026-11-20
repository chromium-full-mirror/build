// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package reapi

import (
	"context"
	"runtime"
	"sync"

	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/metadata"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/o11y/trace"
	"go.chromium.org/build/siso/sync/semaphore"
)

// DefaultUploadLimit is the default number of UploadAll requests in flight
// at once (Option.UploadLimit). It matches remoteexec's digest semaphore
// (10*GOMAXPROCS), which already bounds how many steps can be in
// UploadAll at once, so live requests keep today's parallelism while
// requests abandoned by their requester (e.g. racing losers) queue behind
// them instead of piling up.
func DefaultUploadLimit() int {
	return runtime.GOMAXPROCS(0) * 10
}

// uploadResp is the result of an upload request.
type uploadResp struct {
	numUploaded int
	err         error
}

// uploadRequest is one upload operation: the set of blobs a single
// UploadAll call found to be new (not yet known, not yet pending), to be
// processed independently of the requester's context.
type uploadRequest struct {
	ds   *blob.Store
	ops  map[digest.Digest]*uploadOp
	done chan uploadResp // buffered(1); sent exactly once.

	// md and spanCtx are captured from the submitting requester's context
	// so the detached CAS RPCs carry its gRPC outgoing metadata (including
	// REAPI RequestMetadata with per-action fields such as ActionId and
	// TargetId) and OpenTelemetry trace context. Other actions waiting on
	// the same blobs via knownDigests may share this upload request.
	md      metadata.MD
	spanCtx oteltrace.SpanContext
}

// wait waits for the request to finish, or ctx to be canceled.
// The request keeps running even if ctx is canceled.
func (r *uploadRequest) wait(ctx context.Context) (int, error) {
	select {
	case <-ctx.Done():
		return 0, context.Cause(ctx)
	case resp := <-r.done:
		return resp.numUploaded, resp.err
	}
}

// fail completes the request and all of its ops with err.
func (r *uploadRequest) fail(err error) {
	for _, uop := range r.ops {
		uop.done(err)
	}
	r.done <- uploadResp{err: err}
}

// uploader runs CAS upload requests independently of the steps that
// submit them, with the Client's lifetime context, under the
// "reapi-upload" semaphore that bounds how many are in flight.
//
// Each request runs on its own goroutine, which waits for the semaphore
// and then processes the request; the semaphore's waiters are the queue.
// submit therefore never blocks the requester, and every accepted request
// is answered: by processing, or with the close cause when the uploader
// is closed while the request is waiting or in flight.
type uploader struct {
	c      *Client
	ctx    context.Context
	cancel context.CancelCauseFunc
	sema   *semaphore.Semaphore

	// mu guards closed and serializes wg.Go against Close's wg.Wait.
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func newUploader(ctx context.Context, c *Client, limit int) *uploader {
	if limit <= 0 {
		limit = DefaultUploadLimit()
	}
	ctx, cancel := context.WithCancelCause(ctx)
	return &uploader{
		c:      c,
		ctx:    ctx,
		cancel: cancel,
		sema:   semaphore.New("reapi-upload", limit),
	}
}

// submit hands newBlobs to the uploader as one request and returns
// without waiting for it. Every op in newBlobs is guaranteed to be
// completed eventually.
//
// Outgoing gRPC metadata (such as REAPI RequestMetadata, including
// per-action fields) and OpenTelemetry trace context are captured from
// ctx so the detached upload RPCs carry them even though cancellation
// is detached from ctx. Other actions waiting on the same blobs via
// knownDigests may share this upload request.
func (u *uploader) submit(ctx context.Context, ds *blob.Store, newBlobs map[digest.Digest]*uploadOp) *uploadRequest {
	var md metadata.MD
	if reqMD, ok := metadata.FromOutgoingContext(ctx); ok && len(reqMD) > 0 {
		md = reqMD.Copy()
	}
	req := &uploadRequest{
		ds:      ds,
		ops:     newBlobs,
		done:    make(chan uploadResp, 1),
		md:      md,
		spanCtx: oteltrace.SpanContextFromContext(ctx),
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		req.fail(context.Cause(u.ctx))
		return req
	}
	u.wg.Go(func() {
		err := u.sema.Do(u.ctx, func(ctx context.Context) error {
			u.process(ctx, req)
			return nil
		})
		if err != nil {
			// Closed while waiting for the semaphore.
			req.fail(err)
		}
	})
	return req
}

// process runs one upload request.
func (u *uploader) process(ctx context.Context, req *uploadRequest) {
	if len(req.md) > 0 {
		// Merge onto u.ctx's outgoing metadata, replacing shared keys
		// (such as requestmetadata-bin) rather than appending so we
		// never send duplicate RequestMetadata entries.
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			md = req.md
		} else {
			md = md.Copy()
			for k, v := range req.md {
				md.Set(k, v...)
			}
		}
		ctx = metadata.NewOutgoingContext(ctx, md)
	} else {
		clog.Errorf(ctx, "no metadata in uploadRequest")
	}
	if req.spanCtx.IsValid() {
		ctx = oteltrace.ContextWithSpanContext(ctx, req.spanCtx)
	}
	ctx, span := trace.NewSpan(ctx, "upload-op")
	defer span.Close(nil)
	span.SetAttr("upload", len(req.ops))
	var resp uploadResp
	resp.numUploaded, resp.err = u.c.uploadNewBlobs(ctx, req.ds, req.ops, span)
	if resp.err != nil && u.ctx.Err() != nil {
		// The upload failed because the uploader was closed (or its
		// lifetime ctx canceled); report that rather than the RPC error.
		resp.err = context.Cause(u.ctx)
	}
	req.done <- resp
}

// close cancels waiting and in-flight requests and waits for them to
// finish.
func (u *uploader) close(cause error) {
	u.cancel(cause)
	u.mu.Lock()
	u.closed = true
	u.mu.Unlock()
	u.wg.Wait()
}
