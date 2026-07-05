// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package actioncache

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
)

// TestMigrateLegacySHA256Layout seeds an action cache in the legacy layout
// (SHA-256 results at the data dir root, flat and sharded) and checks that
// opening the cache migrates them into the keyed sha256/ root and they stay
// retrievable, with the startup validation walk enabled.
func TestMigrateLegacySHA256Layout(t *testing.T) {
	casDir := t.TempDir()
	cas, err := blobstore.NewWithOpts(t.Context(), casDir, blobstore.Options{})
	if err != nil {
		t.Fatalf("blobstore.NewWithOpts: %v", err)
	}

	// An empty ActionResult is valid and references no blobs.
	arBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(&repb.ActionResult{})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	// Legacy flat result at the data dir root. The file name is the action's
	// digest, unrelated to the file content.
	flatD := digest.SHA256.FromBytes([]byte("flat action"))
	if err := os.WriteFile(filepath.Join(dir, flatD.Hash), arBytes, 0644); err != nil {
		t.Fatal(err)
	}
	// Legacy sharded result in a {00..ff} dir at the data dir root.
	shardD := digest.SHA256.FromBytes([]byte("sharded action"))
	if err := os.MkdirAll(filepath.Join(dir, shardD.Hash[:2]), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, shardD.Hash[:2], shardD.Hash), arBytes, 0644); err != nil {
		t.Fatal(err)
	}

	ac, err := NewWithOpts(t.Context(), dir, cas, Options{})
	if err != nil {
		t.Fatalf("NewWithOpts: %v", err)
	}

	for _, d := range []digest.Digest{flatD, shardD} {
		if _, err := ac.Get(digest.SHA256, d); err != nil {
			t.Errorf("Get(%s) after migration: %v", d, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "sha256", d.Hash)); err != nil {
			t.Errorf("result %s not at keyed path: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, flatD.Hash)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("legacy flat result still at data dir root: %v", err)
	}
}

// TestDigestFunctionScoping verifies that a default (SHA-256 only) action
// cache provisions no roots for other digest functions and ignores data under
// a non-configured function's root during startup validation.
func TestDigestFunctionScoping(t *testing.T) {
	cas, err := blobstore.NewWithOpts(t.Context(), t.TempDir(), blobstore.Options{})
	if err != nil {
		t.Fatalf("blobstore.NewWithOpts: %v", err)
	}

	dir := t.TempDir()
	// A result-shaped file with garbage content under a root for a function
	// that is not configured. Startup validation must not parse it.
	md5Root := filepath.Join(dir, "md5")
	if err := os.MkdirAll(md5Root, 0755); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(md5Root, "00000000000000000000000000000000")
	if err := os.WriteFile(stray, []byte("not an ActionResult proto"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := NewWithOpts(t.Context(), dir, cas, Options{}); err != nil {
		t.Fatalf("NewWithOpts with stray result under disabled function root: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if got, want := len(entries), 2; got != want {
		t.Errorf("data dir has %d entries %v, want %d (md5 and sha256 only)", got, names, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "sha256")); err != nil {
		t.Errorf("expected sha256/ to exist: %v", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("stray result under disabled function root was not left untouched: %v", err)
	}
}

// TestValidateActionOutputDirectories verifies that validateAction flattens an
// output directory's RootDirectoryDigest (not the action digest) and accepts a
// valid ActionResult referencing an output directory tree.
func TestValidateActionOutputDirectories(t *testing.T) {
	cas, err := blobstore.NewWithOpts(t.Context(), t.TempDir(), blobstore.Options{})
	if err != nil {
		t.Fatalf("blobstore.NewWithOpts: %v", err)
	}
	fn := digest.SHA256

	// Store an output file and a Directory referencing it in the CAS.
	fileDigest, err := cas.Put(fn, []byte("output file content"))
	if err != nil {
		t.Fatalf("cas.Put(file): %v", err)
	}
	dirBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(&repb.Directory{
		Files: []*repb.FileNode{{Name: "f", Digest: fileDigest.Proto()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dirDigest, err := cas.Put(fn, dirBytes)
	if err != nil {
		t.Fatalf("cas.Put(directory): %v", err)
	}

	ac, err := NewWithOpts(t.Context(), t.TempDir(), cas, Options{})
	if err != nil {
		t.Fatalf("NewWithOpts: %v", err)
	}
	actionDigest := fn.FromBytes([]byte("some action"))
	if err := ac.Put(fn, actionDigest, &repb.ActionResult{
		OutputDirectories: []*repb.OutputDirectory{{Path: "out", RootDirectoryDigest: dirDigest.Proto()}},
	}); err != nil {
		t.Fatalf("ac.Put: %v", err)
	}

	blobs, err := ac.validateAction(fn, actionDigest)
	if err != nil {
		t.Fatalf("validateAction: %v", err)
	}
	for _, want := range []digest.Digest{dirDigest, fileDigest} {
		if !slices.Contains(blobs, want) {
			t.Errorf("validateAction blobs = %v, want to contain %s", blobs, want)
		}
	}
}
