// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/toolsupport/abfsutil"
)

func writePastFile(t *testing.T, fname, content string) digest.Digest {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fname, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(fname, past, past); err != nil {
		t.Fatal(err)
	}
	return blob.FromBytes(digest.SHA256, fname, []byte(content)).Digest()
}

// testSockPath returns a short unix domain socket path that stays within
// macOS's 104-byte sockaddr_un.sun_path limit (t.TempDir() embeds the full
// test/subtest name, which can exceed 104 bytes on macOS).
func testSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "abfs") //nolint:usetesting // t.TempDir() embeds the subtest name and exceeds macOS's 104-byte sun_path limit.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// TestDigester_ABFSBatchAndFallback verifies that:
//   - paths outside the ABFS mount directory are split out before calling
//     BatchDigests so an out-of-dir file does not fail the batch;
//   - when BatchDigests returns an error, both background lazyCompute (popQueue)
//     and foreground batchDigester (Entries) fall back inline to local digest
//     computation without spawning goroutines or failing the call.
func TestDigester_ABFSBatchAndFallback(t *testing.T) {
	t.Run("split_out_of_dir", func(t *testing.T) {
		ctx := t.Context()
		abfsDir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		otherDir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		sock := testSockPath(t)
		fake := &abfsutil.Fake{Dir: abfsDir}
		abfs := fake.Start(ctx, t, sock)

		inFile := filepath.Join(abfsDir, "in.txt")
		wantIn := writePastFile(t, inFile, "in-dir content")

		outFile := filepath.Join(otherDir, "out.txt")
		wantOut := writePastFile(t, outFile, "out-of-dir content")

		hfs, err := New(ctx, Option{ABFS: abfs})
		if err != nil {
			t.Fatal(err)
		}
		defer hfs.Close(ctx)

		ents, err := hfs.Entries(ctx, abfsDir, []path.Path{"in.txt", path.New(outFile)})
		if err != nil {
			t.Fatalf("Entries: %v", err)
		}
		if len(ents) != 2 {
			t.Fatalf("len(ents) = %d; want 2", len(ents))
		}
		if got := ents[0].Data.Digest(); got != wantIn {
			t.Errorf("ents[0] digest = %v; want %v", got, wantIn)
		}
		if got := ents[1].Data.Digest(); got != wantOut {
			t.Errorf("ents[1] digest = %v; want %v", got, wantOut)
		}
		if got := fake.GetRBEDigests()["in.txt"]; got != 1 {
			t.Errorf("fake.GetRBEDigests()[in.txt] = %d; want 1", got)
		}
	})

	t.Run("batch_error_inline_fallback", func(t *testing.T) {
		ctx := t.Context()
		abfsDir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		otherDir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}

		var batchCalls atomic.Int32
		sockPath := testSockPath(t)
		lis, err := net.Listen("unix", sockPath)
		if err != nil {
			t.Fatal(err)
		}
		srv := &httptest.Server{
			Listener: lis,
			Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/mnt/get-rbe-digests" {
					batchCalls.Add(1)
				}
				http.Error(w, "simulated abfs failure", http.StatusInternalServerError)
			})},
		}
		srv.Start()
		defer srv.Close()

		abfs, err := abfsutil.New(ctx, "unix://"+sockPath, abfsDir)
		if err != nil {
			t.Fatal(err)
		}
		defer abfs.Close()

		inLazy := filepath.Join(abfsDir, "lazy.txt")
		wantLazy := writePastFile(t, inLazy, "lazy content")
		outLazy := filepath.Join(otherDir, "lazy_out.txt")
		wantOutLazy := writePastFile(t, outLazy, "lazy out-of-dir content")
		inFg := filepath.Join(abfsDir, "fg.txt")
		wantFg := writePastFile(t, inFg, "foreground content")

		hfs, err := New(ctx, Option{ABFS: abfs, DeferDigest: true})
		if err != nil {
			t.Fatal(err)
		}
		defer hfs.Close(ctx)

		// Queue both an in-dir and an out-of-dir file via StatMtime (lazyCompute).
		if _, err := hfs.StatMtime(ctx, abfsDir, "lazy.txt"); err != nil {
			t.Fatal(err)
		}
		if _, err := hfs.StatMtime(ctx, otherDir, "lazy_out.txt"); err != nil {
			t.Fatal(err)
		}

		eLazy, _, _, ok := hfs.directory.lookup(ctx, path.Path(filepath.ToSlash(inLazy)))
		if !ok {
			t.Fatal("entry missing for lazy.txt")
		}
		eOutLazy, _, _, ok := hfs.directory.lookup(ctx, path.Path(filepath.ToSlash(outLazy)))
		if !ok {
			t.Fatal("entry missing for lazy_out.txt")
		}

		deadline := time.Now().Add(2 * time.Second)
		for eLazy.digest() != wantLazy || eOutLazy.digest() != wantOutLazy {
			if time.Now().After(deadline) {
				t.Fatalf("lazy fallback timed out: got lazy=%v (want %v), out=%v (want %v)",
					eLazy.digest(), wantLazy, eOutLazy.digest(), wantOutLazy)
			}
			runtime.Gosched()
		}

		// Also verify foreground batchDigester.wait fallback via Entries.
		ents, err := hfs.Entries(ctx, abfsDir, []path.Path{"fg.txt"})
		if err != nil {
			t.Fatalf("Entries: %v", err)
		}
		if len(ents) != 1 || ents[0].Data.Digest() != wantFg {
			t.Errorf("Entries digest = %v; want %v", ents, wantFg)
		}
		if batchCalls.Load() < 2 {
			t.Errorf("batchCalls = %d; want >= 2", batchCalls.Load())
		}
	})
}

// TestDigester_ABFSStopDrainsQueueNoLeak verifies that stop() drains all
// remaining requests in d.queue synchronously (including when BatchDigests
// fails during stop()) and leaves no leaked goroutines.
func TestDigester_ABFSStopDrainsQueueNoLeak(t *testing.T) {
	ctx := t.Context()
	abfsDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sockPath := testSockPath(t)
	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{
		Listener: lis,
		Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "batch error during stop", http.StatusInternalServerError)
		})},
	}
	srv.Start()
	defer srv.Close()

	abfs, err := abfsutil.New(ctx, "unix://"+sockPath, abfsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer abfs.Close()

	hfs, err := New(ctx, Option{ABFS: abfs})
	if err != nil {
		t.Fatal(err)
	}

	const nFiles = 20
	entries := make([]*entry, nFiles)
	wants := make([]digest.Digest, nFiles)
	// Hold d.mu while enqueueing entries directly so they remain in d.queue
	// until stop() drains them (testing stop()'s synchronous drain and inline
	// fallback after workers exit and d.q is closed).
	hfs.digester.mu.Lock()
	for i := range nFiles {
		name := fmt.Sprintf("f%d.txt", i)
		fname := filepath.Join(abfsDir, name)
		wants[i] = writePastFile(t, fname, fmt.Sprintf("content-%d", i))
		e := newLocalEntry()
		e.init(ctx, fname, hfs.executables, hfs.OS)
		entries[i] = e
		hfs.digester.queue = append(hfs.digester.queue, digestReq{
			fname: fname,
			e:     e,
		})
	}
	hfs.digester.mu.Unlock()

	if err := hfs.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for i, e := range entries {
		if got := e.digest(); got != wants[i] {
			t.Errorf("entries[%d] digest after Close = %v; want %v", i, got, wants[i])
		}
	}
}

// TestDigester_ABFSLocalOutputBypassesABFSAndSemaphore verifies that locally
// generated outputs (e.local == true) hash directly under localDigestSemaphore
// without calling ABFS or blocking on a saturated abfsDigestSemaphore.
func TestDigester_ABFSLocalOutputBypassesABFSAndSemaphore(t *testing.T) {
	ctx := t.Context()
	abfsDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sock := testSockPath(t)
	fake := &abfsutil.Fake{Dir: abfsDir}
	abfs := fake.Start(ctx, t, sock)

	stateDir := t.TempDir()
	hfs, err := New(ctx, Option{
		ABFS:      abfs,
		StateFile: filepath.Join(stateDir, ".siso_fs_state"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hfs.Close(ctx)

	stampPath := filepath.Join(abfsDir, "out", "input_stamp")
	writePastFile(t, stampPath, "")
	depPath := filepath.Join(abfsDir, "out", "input_stamp.d")
	wantDep := writePastFile(t, depPath, "out/input_stamp: foo.jar\n")

	// Saturate every abfsDigestSemaphore slot to prove local outputs do not
	// contend with cold ABFS fetches.
	capN := abfsDigestSemaphore.Capacity()
	releases := make([]func(error), 0, capN)
	defer func() {
		for _, release := range releases {
			release(nil)
		}
	}()
	for range capN {
		_, release, err := abfsDigestSemaphore.WaitAcquire(ctx)
		if err != nil {
			t.Fatalf("WaitAcquire abfsDigestSemaphore: %v", err)
		}
		releases = append(releases, release)
	}

	opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Exercise the local-execution Update -> commitEntry -> journalEntry path.
	updateEnts := hfs.RetrieveUpdateEntriesFromLocal(opCtx, abfsDir, []path.Path{
		"out/input_stamp",
		"out/input_stamp.d",
	})
	if len(updateEnts) != 2 {
		t.Fatalf("len(RetrieveUpdateEntriesFromLocal) = %d; want 2", len(updateEnts))
	}
	now := time.Now()
	for i := range updateEnts {
		updateEnts[i].CmdHash = []byte("cmdhash")
		updateEnts[i].UpdatedTime = now
		updateEnts[i].IsChanged = true
	}
	if err := hfs.Update(opCtx, abfsDir, updateEnts); err != nil {
		t.Fatalf("Update while abfsDigestSemaphore saturated: %v", err)
	}

	// Exercise the foreground Entries (batchDigester) path on a second local
	// output whose digest is not yet computed (e.g. when journalEntry is skipped).
	dep2Path := filepath.Join(abfsDir, "out", "input_stamp2.d")
	wantDep2 := writePastFile(t, dep2Path, "out/input_stamp2: bar.jar\n")
	e2 := newLocalEntry()
	e2.init(opCtx, dep2Path, hfs.executables, hfs.OS)
	e2.local = true
	if _, _, err := hfs.directory.store(opCtx, path.Path(filepath.ToSlash(dep2Path)), e2); err != nil {
		t.Fatalf("store out/input_stamp2.d: %v", err)
	}

	ents, err := hfs.Entries(opCtx, abfsDir, []path.Path{
		"out/input_stamp",
		"out/input_stamp.d",
		"out/input_stamp2.d",
	})
	if err != nil {
		t.Fatalf("Entries while abfsDigestSemaphore saturated: %v", err)
	}
	if len(ents) != 3 {
		t.Fatalf("len(ents) = %d; want 3", len(ents))
	}
	if got, want := ents[0].Data.Digest(), digest.SHA256.Empty(); got != want {
		t.Errorf("input_stamp digest = %v; want %v", got, want)
	}
	if got := ents[1].Data.Digest(); got != wantDep {
		t.Errorf("input_stamp.d digest = %v; want %v", got, wantDep)
	}
	if got := ents[2].Data.Digest(); got != wantDep2 {
		t.Errorf("input_stamp2.d digest = %v; want %v", got, wantDep2)
	}

	// Exercise batchDigestForABFS (lazyCompute/popQueue path) on a local output
	// while abfsDigestSemaphore is still saturated.
	dep3Path := filepath.Join(abfsDir, "out", "input_stamp3.d")
	wantDep3 := writePastFile(t, dep3Path, "out/input_stamp3: baz.jar\n")
	e3 := newLocalEntry()
	e3.init(opCtx, dep3Path, hfs.executables, hfs.OS)
	e3.local = true
	hfs.digester.batchDigestForABFS(opCtx, []digestReq{{fname: dep3Path, e: e3}})
	if got := e3.digest(); got != wantDep3 {
		t.Errorf("input_stamp3.d digest = %v; want %v", got, wantDep3)
	}

	for _, rel := range []string{"out/input_stamp", "out/input_stamp.d", "out/input_stamp2.d", "out/input_stamp3.d"} {
		if got := fake.GetRBEDigests()[rel]; got != 0 {
			t.Errorf("fake.GetRBEDigests()[%q] = %d; want 0", rel, got)
		}
	}
}

type fakeFSLocalSource struct {
	name     string
	content  string
	fsDigest digest.Digest
	fsCalls  atomic.Int32
}

func (*fakeFSLocalSource) IsLocal() {}

func (s *fakeFSLocalSource) Open(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(s.content)), nil
}

func (s *fakeFSLocalSource) String() string {
	return s.name
}

func (s *fakeFSLocalSource) FileDigestFromFS(context.Context) (digest.Digest, error) {
	s.fsCalls.Add(1)
	return s.fsDigest, nil
}

// TestDigester_NoABFSKeepsFileDigestFromFS verifies that non-ABFS backends
// (such as Cog xattr or CartFS) still use FileDigestFromFS when ABFS is nil,
// while ABFS bypasses FileDigestFromFS for locally generated outputs.
func TestDigester_NoABFSKeepsFileDigestFromFS(t *testing.T) {
	ctx := t.Context()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fsDigest := digest.SHA256.FromBytes([]byte("from-fs-digest"))
	localContent := "local-bytes"
	localDigest := digest.SHA256.FromBytes([]byte(localContent))

	hfs, err := New(ctx, Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hfs.Close(ctx)

	fname := filepath.Join(dir, "out", "local.txt")
	src := &fakeFSLocalSource{name: fname, content: localContent, fsDigest: fsDigest}
	e := newLocalEntry()
	e.src = src
	e.local = true
	e.size = int64(len(localContent))
	e.mtime = time.Now().Add(-time.Second)
	e.cmdhash = []byte("cmdhash")

	hfs.digester.compute(ctx, fname, e)
	if got := e.digest(); got != fsDigest {
		t.Errorf("compute without ABFS got digest %v; want %v", got, fsDigest)
	}
	if got := src.fsCalls.Load(); got != 1 {
		t.Errorf("compute without ABFS fsCalls = %d; want 1", got)
	}

	// Verify State() fallback also keeps FileDigestFromFS when ABFS is nil.
	fnameState := filepath.Join(dir, "out", "state.txt")
	srcState := &fakeFSLocalSource{name: fnameState, content: localContent, fsDigest: fsDigest}
	eState := newLocalEntry()
	eState.src = srcState
	eState.local = true
	eState.size = int64(len(localContent))
	eState.mtime = time.Now().Add(-time.Second)
	eState.cmdhash = []byte("cmdhash")
	if _, _, err := hfs.directory.store(ctx, path.Path(filepath.ToSlash(fnameState)), eState); err != nil {
		t.Fatalf("store %s: %v", fnameState, err)
	}
	_ = hfs.State(ctx)
	if got := eState.digest(); got != fsDigest {
		t.Errorf("State without ABFS got digest %v; want %v", got, fsDigest)
	}
	if got := srcState.fsCalls.Load(); got != 1 {
		t.Errorf("State without ABFS fsCalls = %d; want 1", got)
	}

	// With ABFS configured, local outputs must skip FileDigestFromFS and hash
	// local bytes directly.
	sock := testSockPath(t)
	fake := &abfsutil.Fake{Dir: dir}
	abfs := fake.Start(ctx, t, sock)
	hfsABFS, err := New(ctx, Option{ABFS: abfs})
	if err != nil {
		t.Fatal(err)
	}
	defer hfsABFS.Close(ctx)

	srcABFS := &fakeFSLocalSource{name: fname, content: localContent, fsDigest: fsDigest}
	eABFS := newLocalEntry()
	eABFS.src = srcABFS
	eABFS.local = true
	eABFS.size = int64(len(localContent))
	hfsABFS.digester.compute(ctx, fname, eABFS)
	if got := eABFS.digest(); got != localDigest {
		t.Errorf("compute with ABFS got digest %v; want %v", got, localDigest)
	}
	if got := srcABFS.fsCalls.Load(); got != 0 {
		t.Errorf("compute with ABFS fsCalls = %d; want 0", got)
	}
}
