// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"context"
	"runtime"
	"sync"
	"time"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/o11y/trace"
	"go.chromium.org/build/siso/sync/semaphore"
)

// localDigestSemaphore limits concurrent digest calculation on a local file
// system, where a digest reads and hashes the file: CPU- and disk-bound.
var localDigestSemaphore = semaphore.New("file-digest", runtime.GOMAXPROCS(0))

// abfsDigestSemaphore limits concurrent digest calculation on ABFS. There a
// digest is an HTTP call to the ABFS mount, which fetches the blob when its
// cache is cold: latency-bound, so GOMAXPROCS calls in flight leave the mount
// mostly idle, and two-phase caching's first checks wait for the digests of
// thousands of shared inputs (headers) at once.
var abfsDigestSemaphore = semaphore.New("file-digest", 1024)

var noLazyForTests map[string]bool

// SetNoLazyForTest sets filenames that would not calculate digest lazily
// for test.
func SetNoLazyForTest(fnames ...string) {
	if len(fnames) == 0 {
		noLazyForTests = nil
		return
	}
	noLazyForTests = make(map[string]bool)
	for _, fname := range fnames {
		noLazyForTests[fname] = true
	}
}

func localDigest(ctx context.Context, fn digest.Function, src blob.Source, fname string) (blob.Data, error) {
	ctx, span := trace.NewSpan(ctx, "local-digest")
	defer span.Close(nil)

	started := time.Now()
	d, err := blob.FromLocalFile(ctx, fn, src)
	if dur := time.Since(started); dur >= 10*time.Second {
		clog.Warningf(ctx, "too slow local digest %s %s in %s, err=%v", fname, d.Digest(), dur, err)
	}
	return d, err
}

type digestReq struct {
	id    string
	fname string
	e     *entry
}

type digester struct {
	fn        digest.Function
	quitEarly bool
	sema      *semaphore.Semaphore
	q         chan digestReq

	mu    sync.Mutex
	queue []digestReq

	quit chan struct{}
	done chan struct{}
}

// popQueueLocked removes and returns the oldest request in d.queue.
// d.mu must be held and d.queue must not be empty.
//
// It advances the slice instead of shifting it: with a cold cache the
// queue can hold hundreds of thousands of requests, and copying them
// on every pop was O(n) under d.mu. append reallocates when it reaches
// the end of the backing array and copies only the live requests, so
// this is amortized O(1).
func (d *digester) popQueueLocked() digestReq {
	req := d.queue[0]
	d.queue[0] = digestReq{} // don't retain the entry in the backing array.
	d.queue = d.queue[1:]
	return req
}

func (d *digester) start(ctx context.Context) {
	defer close(d.done)
	// one worker fewer than the semaphore's capacity, so a foreground
	// caller of compute (Entries, Flush, Copy, ...) never waits for a slot
	// behind background work alone.
	n := max(d.sema.Capacity()-1, 1)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			d.worker(ctx)
		})
	}
	wg.Wait()
}

func (d *digester) worker(ctx context.Context) {
	for {
		select {
		case <-d.quit:
			return
		case req := <-d.q:
			dctx := ctx
			if req.id != "" {
				dctx = trace.NewContext(dctx, trace.New(dctx, req.id))
			}
			if d.quitEarly {
				dctx, cancel := context.WithCancel(dctx)
				go func() {
					d.compute(dctx, req.fname, req.e)
					cancel()
				}()
				select {
				case <-d.quit:
					cancel()
					return
				case <-dctx.Done():
					// cancel called after d.compute
				}
			} else {
				d.compute(dctx, req.fname, req.e)
			}

			d.mu.Lock()
			if len(d.queue) > 0 {
				select {
				case d.q <- d.queue[0]:
					d.popQueueLocked()
				default:
				}
			}
			d.mu.Unlock()
		}
	}
}

func (d *digester) stop(ctx context.Context) {
	close(d.quit)
	clog.Infof(ctx, "wait for workers")
	<-d.done
	d.mu.Lock()
	q := d.q
	d.q = nil
	d.mu.Unlock()
	close(q)
	clog.Infof(ctx, "run pending digest chan:%d + queue:%d", len(q), len(d.queue))
	if d.quitEarly {
		clog.Infof(ctx, "finish digester early")
		return
	}
	for req := range q {
		dctx := ctx
		if req.id != "" {
			dctx = trace.NewContext(dctx, trace.New(dctx, req.id))
		}
		d.compute(dctx, req.fname, req.e)
	}
	for _, req := range d.queue {
		dctx := ctx
		if req.id != "" {
			dctx = trace.NewContext(dctx, trace.New(dctx, req.id))
		}
		d.compute(dctx, req.fname, req.e)
	}
	d.queue = nil
	clog.Infof(ctx, "finish digester")
}

func (d *digester) lazyCompute(ctx context.Context, fname string, e *entry) {
	e.mu.Lock()
	ed := e.d
	e.mu.Unlock()
	if !ed.IsZero() {
		return
	}
	if noLazyForTests != nil && noLazyForTests[fname] {
		clog.Warningf(ctx, "no lazy for test: %s", fname)
		return
	}
	select {
	case <-ctx.Done():
		clog.Warningf(ctx, "ignore lazyCompute %s: %v", fname, context.Cause(ctx))
		return
	default:
	}
	req := digestReq{
		id:    trace.ID(ctx),
		fname: fname,
		e:     e,
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.q == nil {
		return
	}
	select {
	case d.q <- req:
	default:
		d.queue = append(d.queue, req)
	}
}

func (d *digester) compute(ctx context.Context, fname string, e *entry) {
	e.mu.Lock()
	eErr := e.err
	src := e.src
	ed := e.d
	e.mu.Unlock()
	if eErr != nil || src == nil || !ed.IsZero() {
		return
	}
	err := d.sema.Do(ctx, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			clog.Warningf(ctx, "ignore compute %s: %v", fname, context.Cause(ctx))
			return context.Cause(ctx)
		default:
		}
		return e.compute(ctx, d.fn, fname)
	})
	if err != nil {
		clog.Warningf(ctx, "failed to compute digest %s: %v", fname, err)
	}
}

type batchDigester struct {
	hfs     *HashFS
	wg      sync.WaitGroup
	fnames  []string
	entries []*entry
	nwait   int
}

func (hfs *HashFS) batchDigester() *batchDigester {
	return &batchDigester{
		hfs: hfs,
	}
}

func (b *batchDigester) start(ctx context.Context, fname string, e *entry) {
	e.mu.Lock()
	eErr := e.err
	src := e.src
	ed := e.d
	e.mu.Unlock()
	if eErr != nil || src == nil || !ed.IsZero() {
		return
	}
	b.nwait++
	if b.hfs.opt.ABFS != nil {
		b.fnames = append(b.fnames, fname)
		b.entries = append(b.entries, e)
		return
	}
	b.wg.Go(func() {
		b.hfs.digester.compute(ctx, fname, e)
	})
}

func (b *batchDigester) wait(ctx context.Context) (int, error) {
	if b.hfs.opt.ABFS != nil {
		if len(b.fnames) == 0 {
			return 0, nil
		}
		started := time.Now()
		digests, err := b.hfs.opt.ABFS.BatchDigests(ctx, b.fnames)
		clog.Infof(ctx, "abfs batch digests %d in %s: %v", len(b.fnames), time.Since(started), err)
		if err != nil {
			return 0, err
		}
		for i, d := range digests {
			e := b.entries[i]
			e.mu.Lock()
			e.d = d
			e.mu.Unlock()
		}
		return b.nwait, nil
	}
	b.wg.Wait()
	return b.nwait, nil
}
