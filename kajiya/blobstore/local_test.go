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

	"go.chromium.org/build/kajiya/digest"
)

// putAll puts the given blobs into cas and returns their digests in input
// order so the caller can assert layout-independent retrieval later.
func putAll(t *testing.T, cas *ContentAddressableStorage, blobs [][]byte) []digest.Digest {
	t.Helper()
	digests := make([]digest.Digest, len(blobs))
	for i, b := range blobs {
		d, err := cas.Put(b)
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
		if got, want := cas.Path(d), filepath.Join(dir, d.Hash[:2], d.Hash); got != want {
			t.Errorf("Path(%s) = %q; want %q", d, got, want)
		}
		got, err := cas.Get(d)
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
		if got, want := cas.Path(d), filepath.Join(dir, d.Hash); got != want {
			t.Errorf("Path(%s) = %q; want %q", d, got, want)
		}
		got, err := cas.Get(d)
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
	moved, err := EnsureLayout(dir, true)
	if err != nil {
		t.Fatalf("MigrateLayout: %v", err)
	}
	if moved != 0 {
		t.Errorf("re-migration moved %d entries; want 0", moved)
	}
	for i, d := range digests {
		got, err := cas.Get(d)
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
	d, err := cas.Put(blob)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Plant a junk file at the flat-layout destination to mimic a
	// truncated write left behind by a previous crashed migration.
	stalePath := filepath.Join(dir, d.Hash)
	if err := os.WriteFile(stalePath, []byte("stale junk"), 0644); err != nil {
		t.Fatalf("plant stale dst: %v", err)
	}

	// Reopen in flat mode -- migration should overwrite stalePath with
	// the real, complete content from the shard.
	cas, err = NewWithOpts(t.Context(), dir, Options{Sharded: false})
	if err != nil {
		t.Fatalf("NewWithOpts: %v", err)
	}
	got, err := cas.Get(d)
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
		filepath.Join(dir, d0.Hash[:2], d0.Hash),
		filepath.Join(dir, d0.Hash),
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
		got, err := cas.Get(d)
		if err != nil {
			t.Errorf("Get(%s) after recovery from partial migration: %v", d, err)
			continue
		}
		if !bytes.Equal(got, blobs[i]) {
			t.Errorf("Get(%s) after recovery from partial migration = %q; want %q", d, got, blobs[i])
		}

		// Check that the shard dir for this blob was removed as part of the migration.
		shardDir := filepath.Join(dir, d.Hash[:2])
		if _, err := os.Stat(shardDir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Stat(%s) = %v; want fs.ErrNotExist", shardDir, err)
		}
	}
}
