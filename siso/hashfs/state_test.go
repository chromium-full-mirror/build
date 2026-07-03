// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	mathrand "math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	"go.chromium.org/build/siso/hashfs"
	pb "go.chromium.org/build/siso/hashfs/proto"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/digest"
	"go.chromium.org/build/siso/reapi/merkletree"
)

// mockState returns a mock hashfs state with two entries for testing.
func mockState(t *testing.T) *pb.State {
	t.Helper()

	// Add enough entries to make sure that the state file is > 100kB, so that we exercise
	// enough of any block-based compression code.
	state := &pb.State{}
	randomBytes := make([]byte, 1024)
	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatalf("rand.Read(...)=%v; want nil error", err)
	}
	for i := range 100 {
		state.Entries = append(state.Entries, &pb.Entry{
			Id: &pb.FileID{
				ModTime: int64(i),
			},
			Name:    fmt.Sprintf("file%d", i),
			CmdHash: randomBytes,
		})
	}

	// Ensure that the state file when serialized is > 500kB.
	b, err := proto.Marshal(state)
	if err != nil {
		t.Fatalf("proto.Marshal(%v)=%v; want nil error", state, err)
	}
	if len(b) < 100*1024 {
		t.Fatalf("len(b)=%d; want >100kB", len(b))
	}

	return state
}

func TestLoadMissingStateFile(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	opts := hashfs.Option{
		StateFile:     filepath.Join(dir, ".siso_fs_state"),
		CompressLevel: 1,
	}

	// Handle the case where the state file doesn't exist.
	t.Logf("initial load (fs state doesn't exist)")
	loadedState, err := hashfs.Load(ctx, opts)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load(...)=%v, %v; want %v", loadedState, err, fs.ErrNotExist)
	}
}

func TestLoadSaveEmptyState(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	opts := hashfs.Option{
		StateFile:     filepath.Join(dir, ".siso_fs_state"),
		CompressLevel: 1,
	}

	// Saving an empty state works.
	t.Logf("initial save (empty state)")
	savedState := &pb.State{}
	if err := hashfs.Save(ctx, savedState, opts); err != nil {
		t.Errorf("Save(...)=%v; want nil", err)
	}

	// Loading the saved empty state file works.
	t.Logf("second load (empty state)")
	loadedState, err := hashfs.Load(ctx, opts)
	if err != nil {
		t.Errorf("Load(...)=%v, %v; want nil err", loadedState, err)
	}

	// Loaded state should be equal to the saved state.
	if diff := cmp.Diff(savedState, loadedState, protocmp.Transform()); diff != "" {
		t.Errorf("Load(...) diff -want +got:\n%s", diff)
	}
}

func TestLoadSave(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	savedState := mockState(t)

	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	opts := hashfs.Option{
		StateFile:     filepath.Join(dir, ".siso_fs_state"),
		CompressLevel: 1,
		UseMmap:       true,
	}

	// Save a mock state.
	if err := hashfs.Save(ctx, savedState, opts); err != nil {
		t.Fatalf("Save(...)=%v; want nil", err)
	}

	// Verify the file starts with the zstd magic (empty frame prefix).
	b, err := os.ReadFile(opts.StateFile)
	if err != nil {
		t.Fatalf("Could not read %q: %v", opts.StateFile, err)
	}
	// Zstd magic is 0xFD2FB528 (little-endian: 0x28 0xB5 0x2F 0xFD).
	if got, want := b[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}; !bytes.Equal(got, want) {
		t.Errorf("Save(...) magic = %x, want %x (zstd magic)", got, want)
	}

	// Load the saved state.
	loadedState, err := hashfs.Load(ctx, opts)
	if err != nil {
		t.Fatalf("Load(...)=%v, %v; want nil err", loadedState, err)
	}

	// Compare the loaded state with the saved state.
	if diff := cmp.Diff(savedState, loadedState, protocmp.Transform()); diff != "" {
		t.Errorf("Load(...) diff -want +got:\n%s", diff)
	}
}

// TestLoadLegacyGzip tests that old gzip-compressed state files can still be loaded.
func TestLoadLegacyGzip(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	savedState := mockState(t)

	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	stateFile := filepath.Join(dir, ".siso_fs_state")

	// Manually create a gzip-compressed state file (simulating an old siso version).
	data, err := proto.Marshal(savedState)
	if err != nil {
		t.Fatalf("proto.Marshal(...)=%v; want nil", err)
	}
	var buf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		t.Fatalf("gzip.NewWriterLevel(...)=%v; want nil", err)
	}
	if _, err := gw.Write(data); err != nil {
		t.Fatalf("gzip.Write(...)=%v; want nil", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip.Close(...)=%v; want nil", err)
	}
	if err := os.WriteFile(stateFile, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile(...)=%v; want nil", err)
	}

	// Loading the gzip file should work via backward compat.
	opts := hashfs.Option{
		StateFile:     stateFile,
		CompressLevel: 1,
	}
	loadedState, err := hashfs.Load(ctx, opts)
	if err != nil {
		t.Fatalf("Load(...)=%v, %v; want nil err", loadedState, err)
	}
	if diff := cmp.Diff(savedState, loadedState, protocmp.Transform()); diff != "" {
		t.Errorf("Load(...) diff -want +got:\n%s", diff)
	}
}

func TestState(t *testing.T) {
	ctx := t.Context()

	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	opts := hashfs.Option{
		StateFile:     filepath.Join(dir, ".siso_fs_state"),
		CompressLevel: 1,
	}

	hashFS, err := hashfs.New(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer hashFS.Close(ctx)

	err = hashFS.WaitReady(ctx)
	if err != nil {
		t.Fatalf("WaitReady=%v; want nil", err)
	}

	err = hashFS.WriteFile(ctx, dir, path.Path("stamp"), nil, false, time.Now(), []byte("dummy-cmdhash"), nil)
	if err != nil {
		t.Errorf("WriteFile(...)=%v; want nil error", err)
	}

	st := hashFS.State(ctx)
	m := hashfs.StateMap(st)
	_, ok := m[filepath.ToSlash(filepath.Join(dir, "stamp"))]
	if !ok {
		names := make([]string, 0, len(m))
		for k := range m {
			names = append(names, k)
		}
		sort.Strings(names)
		t.Errorf("stamp entry not exists? %q", names)
	}
}

func TestState_Dir(t *testing.T) {
	ctx := t.Context()

	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	opts := hashfs.Option{
		StateFile:     filepath.Join(dir, ".siso_fs_state"),
		CompressLevel: 1,
	}

	hashFS, err := hashfs.New(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer hashFS.Close(ctx)

	err = hashFS.WaitReady(ctx)
	if err != nil {
		t.Fatalf("WaitReady=%v; want nil", err)
	}

	mtime := time.Now()
	h := sha256.New()
	fmt.Fprint(h, "step command")
	cmdhash := h.Sum(nil)
	cmdhashStr := base64.StdEncoding.EncodeToString(cmdhash)
	d := digest.FromBytes("action digest", []byte("action proto")).Digest()
	t.Logf("record gen/generate_all dir. mtime=%s cmdhash=%s d=%s", mtime, cmdhashStr, d)
	err = update(ctx, hashFS, dir, []merkletree.Entry{
		{
			Name: "gen/generate_all",
		},
	}, mtime, cmdhash, d)
	if err != nil {
		t.Fatalf("Update %v; want nil err", err)
	}

	st := hashFS.State(ctx)
	m := hashfs.StateMap(st)
	ent, ok := m[filepath.ToSlash(filepath.Join(dir, "gen/generate_all"))]
	if !ok {
		t.Errorf("gen/generate_all entry not exists?")
	}
	if ent == nil {
		t.Fatalf("gen/generate_all entry is nil")
	}
	if ent.Id.ModTime != mtime.UnixNano() {
		t.Errorf("mtime=%d want=%d", ent.Id.ModTime, mtime.UnixNano())
	}
	if !bytes.Equal(ent.CmdHash, cmdhash) {
		t.Errorf("cmdhash=%s want=%s", base64.StdEncoding.EncodeToString(ent.CmdHash), cmdhashStr)
	}
	if ent.Action.Hash != d.Hash || ent.Action.SizeBytes != d.SizeBytes {
		t.Errorf("action=%s want=%s", ent.Action, d)
	}
}

func TestState_BadDirEntry(t *testing.T) {
	ctx := t.Context()

	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	opts := hashfs.Option{
		StateFile:     filepath.Join(dir, ".siso_fs_state"),
		CompressLevel: 1,
	}
	mtime := time.Now()
	h := sha256.New()
	fmt.Fprint(h, "step command")
	cmdhash := h.Sum(nil)
	cmdhashStr := base64.StdEncoding.EncodeToString(cmdhash)
	d := digest.FromBytes("action digest", []byte("action proto")).Digest()

	func() {
		t.Logf("-- generate .siso_fs_state for gen/output_file as dir")
		hashFS, err := hashfs.New(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		defer hashFS.Close(ctx)

		err = hashFS.WaitReady(ctx)
		if err != nil {
			t.Fatalf("WaitReady=%v; want nil", err)
		}

		t.Logf("-- record gen/output_file. mtime=%s cmdhash=%s d=%s", mtime, cmdhashStr, d)
		err = update(ctx, hashFS, dir, []merkletree.Entry{
			{
				Name: "gen/output_file",
				// couldn't calculate file's digest.
			},
		}, mtime, cmdhash, d)
		if err != nil {
			t.Fatalf("Update %v; want nil err", err)
		}
	}()

	st, err := hashfs.Load(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	m := hashfs.StateMap(st)
	ent, ok := m[filepath.ToSlash(filepath.Join(dir, "gen/output_file"))]
	if !ok {
		t.Errorf("gen/output_file entry not exists?")
	}
	if ent == nil {
		t.Fatalf("gen/output_file entry is nil")
	}
	if ent.Id.ModTime != mtime.UnixNano() {
		t.Errorf("mtime=%d want=%d", ent.Id.ModTime, mtime.UnixNano())
	}
	if !bytes.Equal(ent.CmdHash, cmdhash) {
		t.Errorf("cmdhash=%s want=%s", base64.StdEncoding.EncodeToString(ent.CmdHash), cmdhashStr)
	}
	if ent.Action.Hash != d.Hash || ent.Action.SizeBytes != d.SizeBytes {
		t.Errorf("action=%s want=%s", ent.Action, d)
	}

	t.Logf("-- ensure gen/output_file exists on disk")
	err = os.MkdirAll(filepath.Join(dir, "gen"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(dir, "gen/output_file"), []byte("output file"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.Chtimes(filepath.Join(dir, "gen/output_file"), time.Time{}, mtime)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("-- gen/output_file is dir in fs_state, but file on disk. should be invalidated")
	hashFS, err := hashfs.New(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer hashFS.Close(ctx)

	err = hashFS.WaitReady(ctx)
	if err != nil {
		t.Fatalf("WaitReady=%v; want nil", err)
	}
	if hashFS.IsClean([]string{}) {
		t.Error("IsClean=true; want false")
	}
	st = hashFS.State(ctx)
	m = hashfs.StateMap(st)
	_, ok = m[filepath.ToSlash(filepath.Join(dir, "gen/output_file"))]
	if ok {
		t.Errorf("gen/output_file entry exists; want not exists")
	}
}

func TestState_Symlink(t *testing.T) {
	ctx := t.Context()

	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	opts := hashfs.Option{
		StateFile:     filepath.Join(dir, ".siso_fs_state"),
		CompressLevel: 1,
	}

	err = os.WriteFile(filepath.Join(dir, "target.0"), []byte("target.0"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(dir, "target.1"), []byte("target.1"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.Symlink("target.0", filepath.Join(dir, "symlink"))
	if err != nil {
		t.Fatal(err)
	}

	func() {
		hashFS, err := hashfs.New(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			err := hashFS.Close(ctx)
			if err != nil {
				t.Errorf("close=%v", err)
			}
		}()
		err = hashFS.WaitReady(ctx)
		if err != nil {
			t.Fatalf("WaitReady=%v; want nil", err)
		}
		fi, err := hashFS.Stat(ctx, dir, path.Path("symlink"))
		if err != nil {
			t.Fatalf("Stat(%q)=%v", "symlink", err)
		}
		if fi.Mode()&fs.ModeSymlink != fs.ModeSymlink {
			t.Errorf("mode=%v; want symlink", fi.Mode())
		}
		if fi.Target() != "target.0" {
			t.Errorf("target=%q; want=%q", fi.Target(), "target.0")
		}
		// make dirty to write state file
		err = hashFS.WriteFile(ctx, dir, path.Path("stamp"), nil, false, time.Now(), []byte("dummy-cmdhash"), nil)
		if err != nil {
			t.Errorf("WriteFile(...)=%v; want nil error", err)
		}
	}()
	st, err := hashfs.Load(ctx, opts)
	if err != nil {
		t.Fatalf("load %v", err)
	}
	m := hashfs.StateMap(st)
	e, ok := m[filepath.ToSlash(filepath.Join(dir, "symlink"))]
	if !ok {
		t.Errorf("no symlnk: %v", m)
	}
	if e.Target != "target.0" {
		t.Errorf("symlink target=%q; want=%q", e.Target, "target.0")
	}

	t.Logf("-- modify symlink")
	err = os.Remove(filepath.Join(dir, "symlink"))
	if err != nil {
		t.Fatal(err)
	}
	err = os.Symlink("target.1", filepath.Join(dir, "symlink"))
	if err != nil {
		t.Fatal(err)
	}
	func() {
		hashFS, err := hashfs.New(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			err := hashFS.Close(ctx)
			if err != nil {
				t.Errorf("close=%v", err)
			}
		}()
		err = hashFS.WaitReady(ctx)
		if err != nil {
			t.Fatalf("WaitReady=%v; want nil", err)
		}
		fi, err := hashFS.Stat(ctx, dir, path.Path("symlink"))
		if err != nil {
			t.Fatalf("Stat(%q)=%v", "symlink", err)
		}
		if fi.Mode()&fs.ModeSymlink != fs.ModeSymlink {
			t.Errorf("mode=%v; want symlink", fi.Mode())
		}
		if fi.Target() != "target.1" {
			t.Errorf("target=%q; want=%q", fi.Target(), "target.1")
		}
	}()
}

// TestState_DirOutput_ReloadPreservesCmdHash verifies that after save/load of a
// directory output's state, every entry (root dir, subdir, inner files) is
// present and still carries the CmdHash marking it a generated output.
//
// The CmdHash gates reconciliation (initFile/initDir, handleBeforeLocal,
// handleAfterLocal); if it regresses, stale or tainted inner files silently
// survive across builds. This drives the entries directly, as the remote path
// does; the local path is covered by e2e TestBuild_DirOutputLocalReloadCmdHash.
func TestState_DirOutput_ReloadPreservesCmdHash(t *testing.T) {
	ctx := t.Context()

	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	opts := hashfs.Option{
		StateFile:     filepath.Join(dir, ".siso_fs_state"),
		CompressLevel: 3,
	}

	// Materialize the tree on disk so reconciliation does not invalidate the
	// entries for a missing local file.
	subdir := filepath.Join(dir, "gendir/sub")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	helloPath := filepath.Join(dir, "gendir/hello.txt")
	nestedPath := filepath.Join(dir, "gendir/sub/nested.txt")
	if err := os.WriteFile(helloPath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nestedPath, []byte("nested"), 0644); err != nil {
		t.Fatal(err)
	}

	mtime := time.Now()
	h := sha256.New()
	fmt.Fprint(h, "dir output step")
	cmdhash := h.Sum(nil)
	actionDg := digest.FromBytes("action digest", []byte("action proto")).Digest()

	helloData := digest.FromBytes("gendir/hello.txt", []byte("hello"))
	nestedData := digest.FromBytes("gendir/sub/nested.txt", []byte("nested"))

	entries := []merkletree.Entry{
		{Name: "gendir"},
		{Name: "gendir/sub"},
		{
			Name: "gendir/hello.txt",
			Data: helloData,
		},
		{
			Name: "gendir/sub/nested.txt",
			Data: nestedData,
		},
	}

	func() {
		hashFS, err := hashfs.New(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := hashFS.Close(ctx); err != nil {
				t.Errorf("close=%v", err)
			}
		}()
		if err := hashFS.WaitReady(ctx); err != nil {
			t.Fatalf("WaitReady=%v; want nil", err)
		}
		if err := update(ctx, hashFS, dir, entries, mtime, cmdhash, actionDg); err != nil {
			t.Fatalf("update=%v; want nil", err)
		}
	}()

	st, err := hashfs.Load(ctx, opts)
	if err != nil {
		t.Fatalf("Load=%v; want nil", err)
	}
	m := hashfs.StateMap(st)
	for _, rel := range []string{"gendir", "gendir/sub", "gendir/hello.txt", "gendir/sub/nested.txt"} {
		key := filepath.ToSlash(filepath.Join(dir, rel))
		ent, ok := m[key]
		if !ok {
			t.Errorf("%s entry not present after reload", rel)
			continue
		}
		if !bytes.Equal(ent.CmdHash, cmdhash) {
			t.Errorf("%s CmdHash=%x; want %x (inner dir-output entries must keep cmdhash for reconciliation)", rel, ent.CmdHash, cmdhash)
		}
	}
}

// TestState_DirOutput_StaleInnerFileReconcile verifies reload reconcile of an
// inner dir-output file externally modified on disk (mtime newer than recorded),
// across both modes:
//
//   - Default (KeepTainted=false): the stale entry is not trusted; a later Stat
//     reflects disk truth, not the recorded mtime.
//   - KeepTainted=true: the cmdhash tag keeps the generated member as tainted
//     (mtime advanced, cmdhash preserved); an untagged member would be dropped,
//     so the tag is load-bearing.
func TestState_DirOutput_StaleInnerFileReconcile(t *testing.T) {
	ctx := t.Context()

	// setup records a cmdhash-tagged member, rewrites it on disk with a newer
	// mtime (the drift the reconcile must notice), then reloads with keepTainted.
	setup := func(t *testing.T, keepTainted bool) (*hashfs.HashFS, string, []byte, time.Time) {
		t.Helper()
		dir := t.TempDir()
		dir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		opts := hashfs.Option{
			StateFile:     filepath.Join(dir, ".siso_fs_state"),
			CompressLevel: 3,
			KeepTainted:   keepTainted,
		}
		if err := os.MkdirAll(filepath.Join(dir, "gendir"), 0755); err != nil {
			t.Fatal(err)
		}
		innerPath := filepath.Join(dir, "gendir/hello.txt")
		if err := os.WriteFile(innerPath, []byte("original"), 0644); err != nil {
			t.Fatal(err)
		}
		recordedMtime := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
		if err := os.Chtimes(innerPath, recordedMtime, recordedMtime); err != nil {
			t.Fatal(err)
		}

		h := sha256.New()
		fmt.Fprint(h, "dir output step")
		cmdhash := h.Sum(nil)
		actionDg := digest.FromBytes("action digest", []byte("action proto")).Digest()
		recordedData := digest.FromBytes("gendir/hello.txt", []byte("original"))

		func() {
			hashFS, err := hashfs.New(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := hashFS.Close(ctx); err != nil {
					t.Errorf("close=%v", err)
				}
			}()
			if err := hashFS.WaitReady(ctx); err != nil {
				t.Fatalf("WaitReady=%v; want nil", err)
			}
			if err := update(ctx, hashFS, dir, []merkletree.Entry{
				{Name: "gendir"},
				{Name: "gendir/hello.txt", Data: recordedData},
			}, recordedMtime, cmdhash, actionDg); err != nil {
				t.Fatalf("update=%v; want nil", err)
			}
		}()

		// recordedMtime+1h, so the disk mtime is unambiguously newer.
		newMtime := recordedMtime.Add(1 * time.Hour)
		if err := os.WriteFile(innerPath, []byte("modified"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(innerPath, newMtime, newMtime); err != nil {
			t.Fatal(err)
		}

		hashFS2, err := hashfs.New(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { hashFS2.Close(ctx) })
		if err := hashFS2.WaitReady(ctx); err != nil {
			t.Fatalf("WaitReady=%v; want nil", err)
		}
		return hashFS2, dir, cmdhash, newMtime
	}

	// Default mode: the stale member is not trusted. A Stat reflects disk
	// truth; dropping the entry entirely is equally acceptable.
	t.Run("DefaultInvalidates", func(t *testing.T) {
		hashFS2, dir, _, newMtime := setup(t, false)
		fi, err := hashFS2.Stat(ctx, dir, "gendir/hello.txt")
		if err != nil {
			if os.IsNotExist(err) {
				return
			}
			t.Fatalf("Stat=%v; want nil or NotExist", err)
		}
		if !fi.ModTime().Equal(newMtime) {
			t.Errorf("post-reload mtime=%v; want %v (disk mtime; reload must not trust the stale recorded mtime)", fi.ModTime(), newMtime)
		}
	})

	// KeepTainted mode: the cmdhash tag keeps the generated member as tainted
	// (mtime advanced, cmdhash preserved) instead of dropping it; an untagged
	// member would be invalidated, so the tag is load-bearing.
	t.Run("KeepTaintedPreservesCmdHash", func(t *testing.T) {
		hashFS2, dir, cmdhash, newMtime := setup(t, true)
		fi, err := hashFS2.Stat(ctx, dir, "gendir/hello.txt")
		if err != nil {
			t.Fatalf("Stat=%v; want nil (a kept-tainted generated entry must survive, not be dropped)", err)
		}
		if !fi.ModTime().Equal(newMtime) {
			t.Errorf("kept-tainted mtime=%v; want %v (disk drift must be reflected)", fi.ModTime(), newMtime)
		}
		m := hashfs.StateMap(hashFS2.State(ctx))
		ent, ok := m[filepath.ToSlash(filepath.Join(dir, "gendir/hello.txt"))]
		if !ok {
			t.Fatalf("gendir/hello.txt absent from state; a kept-tainted generated entry must survive reload")
		}
		if !bytes.Equal(ent.GetCmdHash(), cmdhash) {
			t.Errorf("CmdHash=%x; want %x (the tag that keeps the member as generated output)", ent.GetCmdHash(), cmdhash)
		}
	})
}

// TestState_DirOutput_BuildWithoutBytesReload verifies a directory output
// recorded build-without-bytes (output_local=false: subtree in CAS, only the
// root on disk) survives save/reload with every entry present and unchanged;
// otherwise a consumer's directory input looks changed and re-runs.
func TestState_DirOutput_BuildWithoutBytesReload(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	opts := hashfs.Option{
		StateFile:   filepath.Join(dir, ".siso_fs_state"),
		OutputLocal: func(context.Context, string) bool { return false },
	}
	// Only the declared output root exists on disk (ensureActionOutputDirs
	// pre-creates it); the subtree stays in CAS.
	if err := os.MkdirAll(filepath.Join(dir, "gendir"), 0755); err != nil {
		t.Fatal(err)
	}

	mtime := time.Now()
	h := sha256.New()
	fmt.Fprint(h, "dir output step")
	cmdhash := h.Sum(nil)
	actionDg := digest.FromBytes("action digest", []byte("action proto")).Digest()

	entries := []merkletree.Entry{
		{Name: "gendir"},
		{Name: "gendir/sub"},
		{Name: "gendir/hello.txt", Data: digest.FromBytes("gendir/hello.txt", []byte("hello"))},
		{Name: "gendir/sub/nested.txt", Data: digest.FromBytes("gendir/sub/nested.txt", []byte("nested"))},
	}
	want := []string{"gendir", "gendir/sub", "gendir/hello.txt", "gendir/sub/nested.txt"}

	func() {
		hashFS, err := hashfs.New(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := hashFS.Close(ctx); err != nil {
				t.Errorf("close=%v", err)
			}
		}()
		if err := hashFS.WaitReady(ctx); err != nil {
			t.Fatalf("WaitReady=%v", err)
		}
		if err := update(ctx, hashFS, dir, entries, mtime, cmdhash, actionDg); err != nil {
			t.Fatalf("update=%v", err)
		}
	}()

	// Reload many times: the concurrent storeDirs store can race a parent
	// directory against its child and orphan the subtree (the race the
	// directory.store fix closes). That race is timing-dependent, so a
	// single reload would catch a regression only about half the time.
	for i := range 50 {
		hashFS2, err := hashfs.New(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := hashFS2.WaitReady(ctx); err != nil {
			t.Fatalf("WaitReady=%v", err)
		}
		for _, rel := range want {
			fi, err := hashFS2.Stat(ctx, dir, path.Path(rel))
			if err != nil {
				t.Fatalf("reload %d: %s dropped: %v; a build-without-bytes directory output (output_local=false) must keep every entry across reload even though it is not on local disk", i, rel, err)
			}
			if fi.IsChanged() {
				t.Fatalf("reload %d: %s reloaded as changed; an unchanged build-without-bytes directory output must reload not-changed so the consumer is not re-triggered", i, rel)
			}
		}
		if err := hashFS2.Close(ctx); err != nil {
			t.Errorf("reload %d: close=%v", i, err)
		}
	}
}

// createLargeBenchmarkState builds a protobuf state that approximates real
// Chromium build state data in terms of field population rates, path
// structure, and entry variety. The goal is to produce compression ratios
// close to those observed on real state files.
func createLargeBenchmarkState(tb testing.TB, numEntries int) *pb.State {
	tb.Helper()

	state := &pb.State{}
	state.Entries = make([]*pb.Entry, 0, numEntries)

	// Use a deterministic seed so benchmarks are reproducible.
	rng := mathrand.New(mathrand.NewPCG(42, 0))

	now := time.Now().UnixNano()

	// Word list for generating Chromium-like path components.
	// Directory names and file stems are built by combining 2-3 words.
	words := []string{
		"access", "audio", "base", "bindings", "browser", "build",
		"cache", "chrome", "client", "common", "content", "controller",
		"core", "decoder", "device", "engine", "event", "extension",
		"factory", "file", "frame", "gpu", "handler", "host",
		"impl", "input", "layer", "loader", "manager", "media",
		"model", "network", "platform", "renderer", "resource",
		"scheduler", "service", "stream", "test", "view",
	}

	// pick returns a word from the list using the next random bits.
	pick := func() string {
		return words[rng.IntN(len(words))]
	}

	// combine joins 2-3 words with underscores to build a path component.
	combine := func() string {
		if rng.IntN(3) == 0 {
			return pick() + "_" + pick() + "_" + pick()
		}
		return pick() + "_" + pick()
	}

	exts := []string{
		".h", ".h", ".h", ".h", ".h", // ~26%
		".o", ".o", ".o", ".o", // ~20%
		".cc", ".cc", ".cc", // ~17%
		".js", ".ts", ".ninja", ".pak", ".info", ".cpp",
		".rs", ".gn", ".json", ".c", ".map", ".html",
		".rsp", ".idl", ".css", ".py", ".java", ".xml",
		".svg", ".png", ".txt",
	}

	// Root the synthetic paths at a real temp directory. The children never
	// exist, so SetState's updateFromDisk lstat is a fast ENOENT on every OS.
	// A fixed absolute prefix is not portable: on macOS /home is an autofs
	// automount, so stat'ing paths under /home/... blocks in the automounter.
	prefix := tb.TempDir() + "/"

	// Pre-generate hash pools with realistic reuse rates from real data:
	//   CmdHash:  ~90k unique values shared across ~178k entries (49% reuse)
	//   Action:   ~80k unique values shared across ~134k entries (40% reuse)
	//   Digest:   ~336k unique values across ~374k entries (10% reuse)
	//   EdgeHash: ~91k unique values across ~94k entries (4% reuse, nearly unique)
	cmdHashPool := make([][32]byte, 90_000)
	for j := range cmdHashPool {
		cmdHashPool[j] = sha256.Sum256(fmt.Appendf(nil, "cmd-pool-%d", j))
	}
	actionHashPool := make([][32]byte, 80_000)
	for j := range actionHashPool {
		actionHashPool[j] = sha256.Sum256(fmt.Appendf(nil, "action-pool-%d", j))
	}

	// Generate entries in groups that share a directory, like real build
	// targets that produce multiple .o files in the same obj/ subdirectory.
	// This creates the long shared-prefix patterns that compressors exploit.
	seen := make(map[string]bool, numEntries)
	i := 0
	for i < numEntries {
		// Build a directory path: top/sub/mid, optionally with a leaf.
		top := pick()
		sub := pick()
		mid := combine()
		isBuildOutput := rng.IntN(100) < 52

		// Each group has 5-30 files in the same directory, sharing the
		// same CmdHash and often the same Action (like a real build target).
		groupSize := 5 + rng.IntN(26)
		groupCmdHash := cmdHashPool[rng.IntN(len(cmdHashPool))]
		groupActionHash := actionHashPool[rng.IntN(len(actionHashPool))]

		for g := range groupSize {
			if i >= numEntries {
				break
			}

			stem := combine()
			ext := exts[rng.IntN(len(exts))]

			var name string
			if isBuildOutput {
				if rng.IntN(100) < 60 {
					name = fmt.Sprintf("%sout/debug/obj/%s/%s/%s/%s/%s%s", prefix, top, sub, mid, pick(), stem, ext)
				} else {
					name = fmt.Sprintf("%sout/debug/obj/%s/%s/%s/%s%s", prefix, top, sub, mid, stem, ext)
				}
			} else {
				if rng.IntN(100) < 60 {
					name = fmt.Sprintf("%s%s/%s/%s/%s/%s%s", prefix, top, sub, mid, pick(), stem, ext)
				} else {
					name = fmt.Sprintf("%s%s/%s/%s/%s%s", prefix, top, sub, mid, stem, ext)
				}
			}

			// Skip duplicates — append group-local index if needed.
			if seen[name] {
				name = fmt.Sprintf("%s_%d", name[:len(name)-len(ext)], g) + ext
				if seen[name] {
					continue
				}
			}
			seen[name] = true

			entry := &pb.Entry{
				Id:          &pb.FileID{ModTime: now - rng.Int64N(7*24*3600)*1e9},
				Name:        name,
				UpdatedTime: now - rng.Int64N(3600)*1e9,
			}

			// 99.8% have a digest (10% reuse rate — mostly unique).
			if rng.IntN(1000) < 998 {
				h := sha256.Sum256(fmt.Appendf(nil, "content-%d-%d", i, g))
				entry.Digest = &pb.Digest{
					Hash:      fmt.Sprintf("%x", h),
					SizeBytes: rng.Int64N(10 * 1024 * 1024),
				}
			}

			// CmdHash 47.6%: entries in the same group share the hash.
			if rng.IntN(1000) < 476 {
				entry.CmdHash = groupCmdHash[:]
			}
			// EdgeHash 25.1%: nearly unique per entry.
			if rng.IntN(1000) < 251 {
				h := sha256.Sum256(fmt.Appendf(nil, "edge-%d-%d", i, g))
				entry.EdgeHash = h[:]
			}
			// Action 35.7%: entries in the same group share the action.
			if rng.IntN(1000) < 357 {
				entry.Action = &pb.Digest{
					Hash:      fmt.Sprintf("%x", groupActionHash),
					SizeBytes: rng.Int64N(1024 * 1024),
				}
			}

			if rng.IntN(1000) < 5 {
				entry.IsExecutable = true
			}
			if rng.IntN(1000) < 119 {
				entry.Local = true
			}
			if rng.IntN(1000) < 2 {
				entry.Target = fmt.Sprintf("../target_%d", i%50)
			}

			state.Entries = append(state.Entries, entry)
			i++
		}
	}

	// Sort entries by name, matching real data where entries are ordered
	// alphabetically. This is critical for compression: adjacent entries
	// share long path prefixes, giving LZ77 much better matches.
	sort.Slice(state.Entries, func(i, j int) bool {
		return state.Entries[i].Name < state.Entries[j].Name
	})

	return state
}

func BenchmarkCompression(b *testing.B) {
	st := createLargeBenchmarkState(b, 375_000)
	data, err := proto.Marshal(st)
	if err != nil {
		b.Fatal(err)
	}
	b.Logf("uncompressed state size: %d bytes (%.1f MB, %d entries)", len(data), float64(len(data))/(1024*1024), len(st.Entries))

	levels := []int{1}
	maxThreads := runtime.GOMAXPROCS(0)
	var threadCounts []int
	for n := 1; n <= maxThreads; n *= 2 {
		threadCounts = append(threadCounts, n)
	}
	if threadCounts[len(threadCounts)-1] != maxThreads {
		threadCounts = append(threadCounts, maxThreads)
	}

	type compressionCase struct {
		name string
		opts hashfs.Option
	}
	var cases []compressionCase
	for _, level := range levels {
		for _, threads := range threadCounts {
			cases = append(cases, compressionCase{
				name: fmt.Sprintf("zstd/level=%d/threads=%d", level, threads),
				opts: hashfs.Option{
					CompressLevel:   level,
					CompressThreads: threads,
					UseMmap:         true,
				},
			})
		}
	}

	ctx := b.Context()

	b.Run("save", func(b *testing.B) {
		for _, tc := range cases {
			b.Run(tc.name, func(b *testing.B) {
				benchDir := b.TempDir()
				opts := tc.opts
				opts.StateFile = filepath.Join(benchDir, ".siso_fs_state")
				b.ReportAllocs()
				for b.Loop() {
					if err := hashfs.Save(ctx, st, opts); err != nil {
						b.Fatal(err)
					}
				}
				fi, err := os.Stat(opts.StateFile)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(fi.Size())/(1024*1024), "compressed-MB")
				b.ReportMetric(float64(len(data))/float64(fi.Size()), "ratio")
			})
		}
	})

	b.Run("load", func(b *testing.B) {
		for _, tc := range cases {
			b.Run(tc.name, func(b *testing.B) {
				benchDir := b.TempDir()
				opts := tc.opts
				opts.StateFile = filepath.Join(benchDir, ".siso_fs_state")
				if err := hashfs.Save(ctx, st, opts); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				for b.Loop() {
					loaded, err := hashfs.Load(ctx, opts)
					if err != nil {
						b.Fatal(err)
					}
					if len(loaded.Entries) != len(st.Entries) {
						b.Fatalf("mismatch entries got=%d want=%d", len(loaded.Entries), len(st.Entries))
					}
				}
			})
		}
	})
}

// BenchmarkSetStateLoad loads a large, Chromium-like fs state into a fresh
// HashFS, as build startup does.
func BenchmarkSetStateLoad(b *testing.B) {
	state := createLargeBenchmarkState(b, 400000)
	ctx := b.Context()
	b.ReportAllocs()
	var keep *hashfs.HashFS
	for b.Loop() {
		hfs, err := hashfs.New(ctx, hashfs.Option{})
		if err != nil {
			b.Fatal(err)
		}
		if err := hfs.SetState(ctx, state); err != nil {
			b.Fatal(err)
		}
		if err := hfs.WaitReady(ctx); err != nil {
			b.Fatal(err)
		}
		keep = hfs
	}
	runtime.KeepAlive(keep)
}
