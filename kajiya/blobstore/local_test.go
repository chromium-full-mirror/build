// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// mustFn returns the digest.Function for the given enum value, failing the
// test on unsupported values.
func mustFn(t testing.TB, v repb.DigestFunction_Value) digest.Function {
	t.Helper()
	fn, err := digest.Lookup(v)
	if err != nil {
		t.Fatal(err)
	}
	return fn
}

// putAll puts the given blobs into cas and returns their digests in input
// order so the caller can assert layout-independent retrieval later.
func putAll(t *testing.T, cas *ContentAddressableStorage, blobs [][]byte) []digest.Digest {
	t.Helper()
	digests := make([]digest.Digest, len(blobs))
	for i, b := range blobs {
		d, err := cas.Put(digest.SHA256, b)
		if err != nil {
			t.Fatalf("Put(blob %d): %v", i, err)
		}
		digests[i] = d
	}
	return digests
}

func TestMigrate_FlatToSharded(t *testing.T) {
	dir := t.TempDir()
	blobs := [][]byte{
		[]byte("alpha"),
		[]byte("beta"),
		[]byte("gamma"),
	}

	cas, err := NewWithOpts(t.Context(), dir, Options{Sharded: false})
	if err != nil {
		t.Fatalf("first NewWithOpts: %v", err)
	}
	digests := putAll(t, cas, blobs)

	// Reopen with sharding enabled. Migration should move the blobs into
	// XX/HASH and they should remain retrievable.
	cas, err = NewWithOpts(t.Context(), dir, Options{Sharded: true})
	if err != nil {
		t.Fatalf("second NewWithOpts: %v", err)
	}
	for i, d := range digests {
		if got, want := cas.Path(digest.SHA256, d), filepath.Join(dir, "sha256", d.Hash[:2], d.Hash); got != want {
			t.Errorf("Path(%s) = %q; want %q", d, got, want)
		}
		got, err := cas.Get(digest.SHA256, d)
		if err != nil {
			t.Errorf("Get(%s) after flat->sharded migration: %v", d, err)
			continue
		}
		if !bytes.Equal(got, blobs[i]) {
			t.Errorf("Get(%s) after flat->sharded migration = %q; want %q", d, got, blobs[i])
		}
	}
}

func TestMigrate_ShardedToFlat(t *testing.T) {
	dir := t.TempDir()
	blobs := [][]byte{
		[]byte("alpha"),
		[]byte("beta"),
		[]byte("gamma"),
	}

	cas, err := NewWithOpts(t.Context(), dir, Options{Sharded: true})
	if err != nil {
		t.Fatalf("first NewWithOpts: %v", err)
	}
	digests := putAll(t, cas, blobs)

	cas, err = NewWithOpts(t.Context(), dir, Options{Sharded: false})
	if err != nil {
		t.Fatalf("second NewWithOpts: %v", err)
	}
	for i, d := range digests {
		if got, want := cas.Path(digest.SHA256, d), filepath.Join(dir, "sha256", d.Hash); got != want {
			t.Errorf("Path(%s) = %q; want %q", d, got, want)
		}
		got, err := cas.Get(digest.SHA256, d)
		if err != nil {
			t.Errorf("Get(%s) after sharded->flat migration: %v", d, err)
			continue
		}
		if !bytes.Equal(got, blobs[i]) {
			t.Errorf("Get(%s) after sharded->flat migration = %q; want %q", d, got, blobs[i])
		}
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	blobs := [][]byte{[]byte("alpha"), []byte("beta")}

	cas, err := NewWithOpts(t.Context(), dir, Options{Sharded: true})
	if err != nil {
		t.Fatalf("NewWithOpts: %v", err)
	}
	digests := putAll(t, cas, blobs)

	// Re-running migration on an already-correct layout must be a no-op.
	moved, err := EnsureLayout(filepath.Join(dir, "sha256"), true, digest.SHA256.HexLen())
	if err != nil {
		t.Fatalf("MigrateLayout: %v", err)
	}
	if moved != 0 {
		t.Errorf("re-migration moved %d entries; want 0", moved)
	}
	for i, d := range digests {
		got, err := cas.Get(digest.SHA256, d)
		if err != nil {
			t.Errorf("Get(%s) after idempotent re-migration: %v", d, err)
			continue
		}
		if !bytes.Equal(got, blobs[i]) {
			t.Errorf("Get(%s) after idempotent re-migration = %q; want %q", d, got, blobs[i])
		}
	}
}

// TestMigrate_OverwritesStaleDestination simulates the worst case for
// crash recovery: a partial destination already exists at the target
// path (e.g. leftover from a previous crashed migration where the
// destination write didn't complete) AND the source still exists. The
// next migration must overwrite the stale destination with the source,
// not preserve the half-written destination.
func TestMigrate_OverwritesStaleDestination(t *testing.T) {
	dir := t.TempDir()
	blob := []byte("real content")

	cas, err := NewWithOpts(t.Context(), dir, Options{Sharded: true})
	if err != nil {
		t.Fatalf("seed NewWithOpts: %v", err)
	}
	d, err := cas.Put(digest.SHA256, blob)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Plant a junk file at the flat-layout destination to mimic a
	// truncated write left behind by a previous crashed migration.
	stalePath := filepath.Join(dir, "sha256", d.Hash)
	if err := os.WriteFile(stalePath, []byte("stale junk"), 0644); err != nil {
		t.Fatalf("plant stale dst: %v", err)
	}

	// Reopen in flat mode -- migration should overwrite stalePath with
	// the real, complete content from the shard.
	cas, err = NewWithOpts(t.Context(), dir, Options{Sharded: false})
	if err != nil {
		t.Fatalf("NewWithOpts: %v", err)
	}
	got, err := cas.Get(digest.SHA256, d)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Errorf("after migration, content = %q; want %q (stale dst was not overwritten)", got, blob)
	}
}

// TestMigrate_PartialState simulates a crash mid-migration where some
// blobs already live at the destination layout and others remain in the
// source layout. Both must end up retrievable after the next New().
func TestMigrate_PartialState(t *testing.T) {
	dir := t.TempDir()
	blobs := [][]byte{[]byte("alpha"), []byte("beta"), []byte("gamma")}

	cas, err := NewWithOpts(t.Context(), dir, Options{Sharded: true})
	if err != nil {
		t.Fatalf("seed NewWithOpts: %v", err)
	}
	digests := putAll(t, cas, blobs)

	// Manually flatten the first blob to mimic an interrupted
	// sharded-to-flat migration: one blob already moved up, the rest
	// still in their shards.
	d0 := digests[0]
	if err := os.Rename(
		filepath.Join(dir, "sha256", d0.Hash[:2], d0.Hash),
		filepath.Join(dir, "sha256", d0.Hash),
	); err != nil {
		t.Fatalf("manual partial flatten: %v", err)
	}

	// Reopen in flat mode -- migration should pick up where the crash
	// left off (the remaining shard contents) and finish the job.
	cas, err = NewWithOpts(t.Context(), dir, Options{Sharded: false})
	if err != nil {
		t.Fatalf("NewWithOpts after partial: %v", err)
	}
	for i, d := range digests {
		got, err := cas.Get(digest.SHA256, d)
		if err != nil {
			t.Errorf("Get(%s) after recovery from partial migration: %v", d, err)
			continue
		}
		if !bytes.Equal(got, blobs[i]) {
			t.Errorf("Get(%s) after recovery from partial migration = %q; want %q", d, got, blobs[i])
		}

		// Check that the shard dir for this blob was removed as part of the migration.
		shardDir := filepath.Join(dir, "sha256", d.Hash[:2])
		if _, err := os.Stat(shardDir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Stat(%s) = %v; want fs.ErrNotExist", shardDir, err)
		}
	}
}

// TestDigestFunctionScoping verifies that the CAS provisions and validates
// storage only for the configured digest functions: a default (SHA-256 only)
// CAS creates no roots for other functions, and data under a non-configured
// function's root is ignored rather than validated.
func TestDigestFunctionScoping(t *testing.T) {
	t.Run("default_provisions_sha256_only", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := NewWithOpts(t.Context(), dir, Options{}); err != nil {
			t.Fatalf("NewWithOpts: %v", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if got, want := len(entries), 3; got != want {
			t.Errorf("data dir has %d entries %v, want %d (sha256, splits, and tmp only)", got, names, want)
		}
		for _, want := range []string{"sha256", "splits", "tmp"} {
			if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
				t.Errorf("expected %s/ to exist: %v", want, err)
			}
		}
	})

	t.Run("corrupt_blob_under_disabled_function_ignored", func(t *testing.T) {
		dir := t.TempDir()
		// A blob-shaped file with mismatching content under a root for a
		// function that is not configured. Startup validation must not
		// re-hash (and reject) it.
		md5Root := filepath.Join(dir, "md5")
		if err := os.MkdirAll(md5Root, 0755); err != nil {
			t.Fatal(err)
		}
		corrupt := filepath.Join(md5Root, "00000000000000000000000000000000")
		if err := os.WriteFile(corrupt, []byte("not the md5 of this name"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := NewWithOpts(t.Context(), dir, Options{}); err != nil {
			t.Fatalf("NewWithOpts with corrupt blob under disabled function root: %v", err)
		}
		// The foreign data is left untouched.
		if _, err := os.Stat(corrupt); err != nil {
			t.Errorf("blob under disabled function root was not left untouched: %v", err)
		}
	})

	t.Run("configured_functions_are_provisioned", func(t *testing.T) {
		dir := t.TempDir()
		blake3Fn := mustFn(t, repb.DigestFunction_BLAKE3)
		fns := []digest.Function{digest.SHA256, blake3Fn}
		cas, err := NewWithOpts(t.Context(), dir, Options{DigestFunctions: fns})
		if err != nil {
			t.Fatalf("NewWithOpts: %v", err)
		}
		// Both roots exist and hold the seeded empty blob.
		for _, fn := range fns {
			if !cas.Has(fn, fn.Empty()) {
				t.Errorf("empty blob for %v missing", fn)
			}
			if _, err := os.Stat(filepath.Join(dir, fn.String())); err != nil {
				t.Errorf("expected root for %v: %v", fn, err)
			}
		}
		// No roots for functions outside the configured set.
		if _, err := os.Stat(filepath.Join(dir, "sha512")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("unexpected sha512 root (err = %v), want fs.ErrNotExist", err)
		}
	})
}

// TestMultiFunction stores blobs under several digest functions, checks their
// on-disk layout (each function under its own <function>/ root), and confirms
// the validation walk passes when the CAS is reopened over the mixed content.
func TestMultiFunction(t *testing.T) {
	for _, sharded := range []bool{false, true} {
		t.Run(map[bool]string{false: "flat", true: "sharded"}[sharded], func(t *testing.T) {
			dir := t.TempDir()
			fns := []digest.Function{
				mustFn(t, repb.DigestFunction_SHA256),
				mustFn(t, repb.DigestFunction_SHA1),
				mustFn(t, repb.DigestFunction_GITSHA1),
				mustFn(t, repb.DigestFunction_BLAKE3),
			}
			cas, err := NewWithOpts(t.Context(), dir, Options{Sharded: sharded, DigestFunctions: fns})
			if err != nil {
				t.Fatalf("NewWithOpts: %v", err)
			}
			digests := make(map[digest.Function]digest.Digest)
			blob := []byte("multi-function content")
			for _, fn := range fns {
				d, err := cas.Put(fn, blob)
				if err != nil {
					t.Fatalf("Put(%v): %v", fn, err)
				}
				digests[fn] = d

				// Verify the on-disk location is namespaced by function.
				root := filepath.Join(dir, fn.String())
				want := filepath.Join(root, d.Hash)
				if sharded {
					want = filepath.Join(root, d.Hash[:2], d.Hash)
				}
				if got := cas.Path(fn, d); got != want {
					t.Errorf("Path(%v) = %q, want %q", fn, got, want)
				}
				if _, err := os.Stat(want); err != nil {
					t.Errorf("blob for %v not at %q: %v", fn, want, err)
				}
			}

			// sha1 and sha256 produce different hashes for the same content.
			if digests[fns[1]].Hash == digests[fns[0]].Hash {
				t.Error("sha1 and sha256 unexpectedly produced the same hash")
			}

			// Reopen over the mixed content: the validation walk must accept
			// all functions and re-hash each with the correct function.
			cas, err = NewWithOpts(t.Context(), dir, Options{Sharded: sharded, DigestFunctions: fns})
			if err != nil {
				t.Fatalf("reopen NewWithOpts: %v", err)
			}
			for _, fn := range fns {
				got, err := cas.Get(fn, digests[fn])
				if err != nil {
					t.Errorf("Get(%v) after reopen: %v", fn, err)
					continue
				}
				if !bytes.Equal(got, blob) {
					t.Errorf("Get(%v) = %q, want %q", fn, got, blob)
				}
			}
		})
	}
}

// TestMigrateLegacySHA256Layout seeds a data dir in the legacy layout (SHA-256
// blobs at the root, flat and sharded, next to a keyed blake3 root) and checks
// that opening the CAS migrates everything into the keyed sha256/ root, for
// both target layouts, with validation enabled so every migrated blob is
// re-hashed.
func TestMigrateLegacySHA256Layout(t *testing.T) {
	blake3Fn := mustFn(t, repb.DigestFunction_BLAKE3)

	for _, sharded := range []bool{false, true} {
		t.Run(map[bool]string{false: "flat", true: "sharded"}[sharded], func(t *testing.T) {
			dir := t.TempDir()

			// Legacy flat blob at the data dir root.
			flatContent := []byte("legacy flat blob")
			flatD := digest.SHA256.FromBytes(flatContent)
			if err := os.WriteFile(filepath.Join(dir, flatD.Hash), flatContent, 0644); err != nil {
				t.Fatal(err)
			}

			// Legacy sharded blob in a {00..ff} dir at the data dir root.
			shardContent := []byte("legacy sharded blob")
			shardD := digest.SHA256.FromBytes(shardContent)
			if err := os.MkdirAll(filepath.Join(dir, shardD.Hash[:2]), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, shardD.Hash[:2], shardD.Hash), shardContent, 0644); err != nil {
				t.Fatal(err)
			}

			// An already-keyed blake3 blob that must be left in place.
			b3Content := []byte("keyed blake3 blob")
			b3D := blake3Fn.FromBytes(b3Content)
			if err := os.MkdirAll(filepath.Join(dir, "blake3"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "blake3", b3D.Hash), b3Content, 0644); err != nil {
				t.Fatal(err)
			}

			fns := []digest.Function{digest.SHA256, blake3Fn}
			cas, err := NewWithOpts(t.Context(), dir, Options{Sharded: sharded, DigestFunctions: fns})
			if err != nil {
				t.Fatalf("NewWithOpts: %v", err)
			}

			for _, tc := range []struct {
				fn      digest.Function
				d       digest.Digest
				content []byte
			}{
				{digest.SHA256, flatD, flatContent},
				{digest.SHA256, shardD, shardContent},
				{blake3Fn, b3D, b3Content},
			} {
				if got, err := cas.Get(tc.fn, tc.d); err != nil || !bytes.Equal(got, tc.content) {
					t.Errorf("Get(%v, %s) = %q, %v; want %q", tc.fn, tc.d, got, err, tc.content)
				}
				if _, err := os.Stat(cas.Path(tc.fn, tc.d)); err != nil {
					t.Errorf("blob %s not at keyed path %q: %v", tc.d, cas.Path(tc.fn, tc.d), err)
				}
			}

			// The legacy locations must be gone.
			if _, err := os.Stat(filepath.Join(dir, flatD.Hash)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("legacy flat blob still at data dir root: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, shardD.Hash[:2], shardD.Hash)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("legacy sharded blob still at data dir root: %v", err)
			}

			// Reopening must be a no-op migration and still validate.
			if _, err := NewWithOpts(t.Context(), dir, Options{Sharded: sharded, DigestFunctions: fns}); err != nil {
				t.Fatalf("reopen NewWithOpts: %v", err)
			}
		})
	}
}

// TestMigrateLegacySHA256LeftoverShardDir covers a legacy shard directory that
// cannot be fully migrated because it contains a file that is not a blob: the
// blobs are moved, the stray file and its directory are left in place, and the
// junk-only directory is not counted as a migrated entry.
func TestMigrateLegacySHA256LeftoverShardDir(t *testing.T) {
	dir := t.TempDir()
	fnRoot := filepath.Join(dir, "sha256")

	blob := []byte("legacy blob next to junk")
	blobD := digest.SHA256.FromBytes(blob)
	blobShard := blobD.Hash[:2]
	junkShard := "aa"
	if junkShard == blobShard {
		junkShard = "bb"
	}

	// Legacy shard dir with a real blob and a stray file. The migrated
	// destination shard already exists, forcing the per-blob fallback.
	for _, p := range []string{
		filepath.Join(dir, blobShard),
		filepath.Join(dir, junkShard),
		filepath.Join(fnRoot, blobShard),
		filepath.Join(fnRoot, junkShard),
	} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, blobShard, blobD.Hash), blob, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, junkShard, "junk.txt"), []byte("junk"), 0644); err != nil {
		t.Fatal(err)
	}

	moved, err := MigrateLegacySHA256(dir, fnRoot, digest.SHA256.HexLen())
	if err != nil {
		t.Fatalf("MigrateLegacySHA256: %v", err)
	}
	// Only the shard dir that had a blob counts as migrated; the junk-only
	// dir must not be counted (nothing was moved out of it).
	if got, want := moved, 1; got != want {
		t.Errorf("MigrateLegacySHA256 moved = %d, want %d", got, want)
	}

	// The blob is at its keyed location.
	if _, err := os.Stat(filepath.Join(fnRoot, blobShard, blobD.Hash)); err != nil {
		t.Errorf("migrated blob not at keyed path: %v", err)
	}
	// The stray file and its legacy shard dir remain untouched.
	if _, err := os.Stat(filepath.Join(dir, junkShard, "junk.txt")); err != nil {
		t.Errorf("stray file was not left in place: %v", err)
	}
}

// TestMigrateLegacySHA256Interrupted covers resuming a half-done migration:
// the keyed root already holds a shard dir with one blob while the same shard
// at the legacy root still holds another.
func TestMigrateLegacySHA256Interrupted(t *testing.T) {
	dir := t.TempDir()

	// Two blobs whose hashes share a shard prefix would be ideal, but any two
	// work: one already migrated into sha256/<shard>/, one still legacy.
	migrated := []byte("already migrated blob")
	migratedD := digest.SHA256.FromBytes(migrated)
	if err := os.MkdirAll(filepath.Join(dir, "sha256", migratedD.Hash[:2]), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sha256", migratedD.Hash[:2], migratedD.Hash), migrated, 0644); err != nil {
		t.Fatal(err)
	}

	legacy := []byte("still legacy blob")
	legacyD := digest.SHA256.FromBytes(legacy)
	// Force the per-blob fallback by making the legacy shard collide with an
	// existing keyed shard dir.
	if err := os.MkdirAll(filepath.Join(dir, legacyD.Hash[:2]), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, legacyD.Hash[:2], legacyD.Hash), legacy, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sha256", legacyD.Hash[:2]), 0755); err != nil {
		t.Fatal(err)
	}

	cas, err := NewWithOpts(t.Context(), dir, Options{Sharded: true})
	if err != nil {
		t.Fatalf("NewWithOpts: %v", err)
	}
	for _, tc := range []struct {
		d       digest.Digest
		content []byte
	}{
		{migratedD, migrated},
		{legacyD, legacy},
	} {
		if got, err := cas.Get(digest.SHA256, tc.d); err != nil || !bytes.Equal(got, tc.content) {
			t.Errorf("Get(%s) = %q, %v; want %q", tc.d, got, err, tc.content)
		}
	}
}
