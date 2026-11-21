// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package merkletree

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"slices"
	"testing"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/path"
)

// randomTreeInputs returns random entries and subtrees for a tree, with
// nested and shared directories, executables, duplicates, symlinks
// (including symlinks to directories that have entries) and precomputed
// subtrees.
func randomTreeInputs(rnd *rand.Rand, fn digest.Function) ([]Entry, []TreeEntry) {
	dirs := []string{"a", "a/b", "a/b/c", "d", "d/e", "f/g/h/i", "out/soong/.intermediates/x"}
	n := 1 + rnd.Intn(400)
	var ents []Entry
	for range n {
		dir := dirs[rnd.Intn(len(dirs))]
		name := path.Path(fmt.Sprintf("%s/f%d", dir, rnd.Intn(n)))
		switch r := rnd.Intn(40); {
		case r < 2:
			ents = append(ents, Entry{Name: path.Path(dir)})
		case r < 4:
			ents = append(ents, Entry{Name: path.Path(fmt.Sprintf("%s/l%d", dir, rnd.Intn(5))), Target: "../t"})
		case r == 4:
			// symlink to a directory, and an entry under it.
			ents = append(ents, Entry{Name: "a/b/lc", Target: "c"})
			ents = append(ents, Entry{Name: path.Path(fmt.Sprintf("a/b/lc/g%d", rnd.Intn(5))), Data: blob.FromBytes(fn, "x", []byte("g"))})
		default:
			content := fmt.Sprintf("c%d", rnd.Intn(n))
			e := Entry{Name: name, Data: blob.FromBytes(fn, "x", []byte(content)), IsExecutable: rnd.Intn(5) == 0}
			ents = append(ents, e)
			if rnd.Intn(10) == 0 {
				ents = append(ents, e) // identical duplicate collapses
			}
		}
	}
	// files with the same name must have the same content; drop conflicts.
	seen := map[path.Path]Entry{}
	var clean []Entry
	for _, e := range ents {
		if p, ok := seen[e.Name]; ok && (p.Data.Digest() != e.Data.Digest() || p.IsExecutable != e.IsExecutable || p.Target != e.Target) {
			continue
		}
		seen[e.Name] = e
		clean = append(clean, e)
	}
	if rnd.Intn(3) == 0 {
		slices.SortFunc(clean, func(a, b Entry) int { return cmp.Compare(a.Name, b.Name) })
	}
	var subtrees []TreeEntry
	if rnd.Intn(4) == 0 {
		subtrees = append(subtrees, TreeEntry{Name: "pre/computed", Digest: fn.FromBytes([]byte("subtree"))})
	}
	return clean, subtrees
}

func buildRandomTree(ctx context.Context, mt *MerkleTree, ents []Entry, subtrees []TreeEntry) (digest.Digest, error) {
	for _, st := range subtrees {
		if err := mt.SetTree(st); err != nil {
			return digest.Digest{}, err
		}
	}
	for _, e := range ents {
		if err := mt.Set(e); err != nil {
			return digest.Digest{}, err
		}
	}
	return mt.Build(ctx)
}

// TestBuildDigestsGolden pins the root digests and the stored directory
// blobs of random trees, built by New and NewPooled, with and without a
// store, and with pooled trees reused, so that a merkletree change that
// changes a digest fails this test.
func TestBuildDigestsGolden(t *testing.T) {
	ctx := t.Context()
	fn := digest.SHA256
	h := sha256.New()
	for seed := int64(1); seed <= 300; seed++ {
		ents, subtrees := randomTreeInputs(rand.New(rand.NewSource(seed)), fn)
		var results []string
		for _, mode := range []string{"new", "new+store", "pooled", "pooled+store", "pooled", "pooled+store"} {
			var ds *blob.Store
			if mode == "new+store" || mode == "pooled+store" {
				ds = blob.NewStore()
			}
			var mt *MerkleTree
			if mode == "new" || mode == "new+store" {
				mt = New(fn, ds)
			} else {
				mt = NewPooled(fn, ds)
			}
			d, err := buildRandomTree(ctx, mt, ents, subtrees)
			mt.Release()
			r := fmt.Sprintf("%s err=%t", d, err != nil)
			if ds != nil {
				blobs := ds.List()
				slices.SortFunc(blobs, func(a, b digest.Digest) int { return cmp.Compare(a.Hash, b.Hash) })
				for _, bd := range blobs {
					data, _ := ds.Get(bd)
					b, err := blob.DataToBytes(ctx, data)
					if err != nil {
						t.Fatal(err)
					}
					r += fmt.Sprintf(" %s:%x", bd, sha256.Sum256(b))
				}
			}
			results = append(results, r)
		}
		// new and pooled trees must agree.
		if results[0] != results[2] || results[0] != results[4] || results[1] != results[3] || results[1] != results[5] {
			t.Fatalf("seed %d: new and pooled trees differ:\n%q", seed, results)
		}
		fmt.Fprintf(h, "%d %q\n", seed, results)
	}
	const want = "58b98de155c4b69dfd26c21d93576491fd803705dbc600c9712cb660eb48199b"
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		t.Errorf("digests of random trees = %s; want %s", got, want)
	}
}

// BenchmarkTreeDigest builds a pooled tree of 3000 input files in 60
// directories, as Cmd.Digest does for each step: with a nil store for the
// 2PC lookup key, and with a store for remote execution.
func BenchmarkTreeDigest(b *testing.B) {
	ctx := b.Context()
	fn := digest.SHA256
	var ents []Entry
	for d := range 60 {
		for f := range 50 {
			name := fmt.Sprintf("frameworks/base/core/dir%02d/sub%d/file%03d.h", d, d%3, f)
			ents = append(ents, Entry{Name: path.Path(name), Data: blob.FromBytes(fn, name, []byte(name))})
		}
	}
	for _, bc := range []struct {
		name  string
		store bool
	}{
		{"nil_store", false},
		{"store", true},
	} {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var ds *blob.Store
				if bc.store {
					ds = blob.NewStore()
				}
				mt := NewPooled(fn, ds)
				_, err := buildRandomTree(ctx, mt, ents, nil)
				mt.Release()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
