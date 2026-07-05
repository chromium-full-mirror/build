// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package localexec

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	iradix "github.com/hashicorp/go-immutable-radix/v2"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/model"
)

// TestTreeRepositoryDigestFunctionNamespacing verifies that materialized trees
// are namespaced by digest function: the same directory hash under two
// different functions (e.g. SHA-256 and SHA256TREE, which agree on all blobs
// up to 1024 bytes) must not share a tree-cache entry.
func TestTreeRepositoryDigestFunctionNamespacing(t *testing.T) {
	sha256treeFn, err := digest.ParseFunction("sha256tree")
	if err != nil {
		t.Fatal(err)
	}
	fns := []digest.Function{digest.SHA256, sha256treeFn}

	cas, err := blobstore.NewWithOpts(t.Context(), t.TempDir(), blobstore.Options{DigestFunctions: fns})
	if err != nil {
		t.Fatalf("blobstore.NewWithOpts: %v", err)
	}

	// One file digest, two different contents: plant them directly at each
	// function's CAS path so a mixed-up materialization is observable.
	contents := map[digest.Function]string{
		digest.SHA256: "content-sha256\n",
		sha256treeFn:  "content-sha256tree\n",
	}
	fileDigest := digest.Digest{Hash: strings.Repeat("ab", 32), SizeBytes: int64(len(contents[digest.SHA256]))}
	for fn, content := range contents {
		if err := os.WriteFile(cas.Path(fn, fileDigest), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// A trie with a single root directory containing the file. The root's
	// directory digest is the same hash string under both functions.
	rootDigest := digest.Digest{Hash: strings.Repeat("cd", 32), SizeBytes: 42}
	txn := iradix.New[*model.KajiyaDirectory]().Txn()
	txn.Insert([]byte(""), &model.KajiyaDirectory{
		Digest: rootDigest,
		Files: []model.KajiyaFile{
			{Name: "f", Digest: fileDigest, UnixMode: 0644},
		},
		UnixMode: 0755,
	})
	trie := txn.Commit()

	treesDir := t.TempDir()
	trees, err := newTreeRepository(filepath.Join(treesDir, "trees"), cas)
	if err != nil {
		t.Fatalf("newTreeRepository: %v", err)
	}

	for _, fn := range fns {
		if err := trees.EnsureDirectory(fn, trie); err != nil {
			t.Fatalf("EnsureDirectory(%v): %v", fn, err)
		}
	}

	// Both functions' contents must be materialized in distinct tree-cache
	// entries; a shared entry would serve the first function's content for
	// both.
	found := map[string]int{}
	err = filepath.WalkDir(treesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "f" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		found[string(data)]++
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	for _, fn := range fns {
		if got, want := found[contents[fn]], 1; got != want {
			t.Errorf("materialized %d copies of the %v content, want %d (found: %v)", got, fn, want, found)
		}
	}
}
