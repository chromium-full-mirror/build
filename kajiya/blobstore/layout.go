// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"go.chromium.org/build/hashigo/digest"
)

// EnsureLayout creates the necessary directory structure for a hash-addressed
// storage. Existing blobs are migrated without data loss. Returns the number
// of blobs moved during migration (if any).
func EnsureLayout(dataDir string, sharded bool) (moved int, err error) {
	var layout string
	if sharded {
		moved, err = ensureSharded(dataDir)
		layout = "sharded"
	} else {
		moved, err = ensureFlat(dataDir)
		layout = "flat"
	}
	if err != nil {
		return moved, err
	}
	if moved > 0 {
		slog.Info("migrated data layout", "path", dataDir, "moved_blobs", moved, "target_layout", layout)
	}
	return moved, nil
}

// ensureSharded creates {00..ff} shard directories and moves any top-level
// blob files into their corresponding shard.
func ensureSharded(dataDir string) (int, error) {
	var moved int

	// Create subdirectories {00, 01, ..., ff} for sharding by hash prefix.
	for i := range 256 {
		err := os.Mkdir(filepath.Join(dataDir, fmt.Sprintf("%02x", i)), 0755)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return moved, err
		}
	}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !looksLikeBlobName(name) {
			continue
		}

		src := filepath.Join(dataDir, name)
		dst := filepath.Join(dataDir, name[:2], name)
		if err := os.Rename(src, dst); err != nil {
			return moved, err
		}
		moved++
	}

	return moved, nil
}

// ensureFlat moves blobs out of any existing shard directories up into dataDir
// and removes the now-empty shard dirs.
func ensureFlat(dataDir string) (int, error) {
	var moved int

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !looksLikeShardDir(name) {
			continue
		}

		shardDir := filepath.Join(dataDir, name)
		blobs, err := os.ReadDir(shardDir)
		if err != nil {
			return moved, err
		}

		for _, b := range blobs {
			if b.IsDir() || !looksLikeBlobName(b.Name()) {
				continue
			}
			src := filepath.Join(shardDir, b.Name())
			dst := filepath.Join(dataDir, b.Name())
			if err := os.Rename(src, dst); err != nil {
				return moved, err
			}
			moved++
		}

		// Best-effort cleanup of the now-empty shard dir; non-empty
		// (unexpected contents) is left for validate() to flag.
		_ = os.Remove(shardDir)
	}

	return moved, nil
}

func looksLikeShardDir(name string) bool {
	return len(name) == 2 && digest.IsHex(name[0]) && digest.IsHex(name[1])
}

func looksLikeBlobName(name string) bool {
	if len(name) != digest.SHA256.HexLen() {
		return false
	}
	for i := range len(name) {
		if !digest.IsHex(name[i]) {
			return false
		}
	}
	return true
}
