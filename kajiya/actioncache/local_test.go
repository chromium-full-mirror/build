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

// TestValidateActionOutputDirectoryDigests verifies that validateAction
// accepts an output directory described by TreeDigest, RootDirectoryDigest, or
// both, reports the blobs each form references, and rejects an output directory
// with neither digest, with a Tree blob that is missing from the CAS, or with a
// Tree that lists a file that is missing from the CAS.
func TestValidateActionOutputDirectoryDigests(t *testing.T) {
	// Create a CAS.
	cas, err := blobstore.NewWithOpts(t.Context(), t.TempDir(), blobstore.Options{})
	if err != nil {
		t.Fatalf("blobstore.NewWithOpts: %v", err)
	}
	fn := digest.SHA256

	// Store an output file, a Directory referencing it, and the equivalent
	// Tree in the CAS.
	fileDigest, err := cas.Put(fn, []byte("output file content"))
	if err != nil {
		t.Fatalf("cas.Put(file): %v", err)
	}
	dir := &repb.Directory{
		Files: []*repb.FileNode{{Name: "f", Digest: fileDigest.Proto()}},
	}
	dirBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(dir)
	if err != nil {
		t.Fatal(err)
	}
	dirDigest, err := cas.Put(fn, dirBytes)
	if err != nil {
		t.Fatalf("cas.Put(directory): %v", err)
	}
	treeBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(&repb.Tree{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	treeDigest, err := cas.Put(fn, treeBytes)
	if err != nil {
		t.Fatalf("cas.Put(tree): %v", err)
	}
	missingDigest := fn.FromBytes([]byte("never stored"))
	missingFileTreeBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(&repb.Tree{
		Root: &repb.Directory{Files: []*repb.FileNode{{Name: "f", Digest: missingDigest.Proto()}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	missingFileTreeDigest, err := cas.Put(fn, missingFileTreeBytes)
	if err != nil {
		t.Fatalf("cas.Put(tree with missing file): %v", err)
	}

	// Open an action cache backed by that CAS.
	ac, err := NewWithOpts(t.Context(), t.TempDir(), cas, Options{})
	if err != nil {
		t.Fatalf("NewWithOpts: %v", err)
	}

	for _, tc := range []struct {
		name      string
		outDir    *repb.OutputDirectory
		wantErr   bool
		wantBlobs []digest.Digest
	}{
		{
			name:      "tree_digest_only",
			outDir:    &repb.OutputDirectory{Path: "out", TreeDigest: treeDigest.Proto()},
			wantBlobs: []digest.Digest{treeDigest, fileDigest},
		},
		{
			name:      "root_directory_digest_only",
			outDir:    &repb.OutputDirectory{Path: "out", RootDirectoryDigest: dirDigest.Proto()},
			wantBlobs: []digest.Digest{dirDigest, fileDigest},
		},
		{
			name: "both_digests",
			outDir: &repb.OutputDirectory{
				Path:                "out",
				TreeDigest:          treeDigest.Proto(),
				RootDirectoryDigest: dirDigest.Proto(),
			},
			wantBlobs: []digest.Digest{treeDigest, dirDigest, fileDigest},
		},
		{
			name:    "neither_digest",
			outDir:  &repb.OutputDirectory{Path: "out"},
			wantErr: true,
		},
		{
			name:    "missing_tree_blob",
			outDir:  &repb.OutputDirectory{Path: "out", TreeDigest: missingDigest.Proto()},
			wantErr: true,
		},
		{
			name:    "missing_file_in_tree",
			outDir:  &repb.OutputDirectory{Path: "out", TreeDigest: missingFileTreeDigest.Proto()},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Cache a result with the output directory and validate it.
			actionDigest := fn.FromBytes([]byte(tc.name))
			if err := ac.Put(fn, actionDigest, &repb.ActionResult{
				OutputDirectories: []*repb.OutputDirectory{tc.outDir},
			}); err != nil {
				t.Fatalf("ac.Put: %v", err)
			}
			blobs, err := ac.validateAction(fn, actionDigest)
			if got, want := err != nil, tc.wantErr; got != want {
				t.Fatalf("validateAction: got error %v, want error: %t", err, want)
			}

			// Check that the referenced blobs are reported.
			for _, want := range tc.wantBlobs {
				if !slices.Contains(blobs, want) {
					t.Errorf("validateAction blobs = %v, want to contain %s", blobs, want)
				}
			}
		})
	}
}
