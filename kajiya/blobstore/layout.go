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
	"strings"

	"go.chromium.org/build/hashigo/digest"
)

// EnsureFunctionRoots creates each configured digest function's root directory
// (<function>/ under dataDir) with the requested flat/sharded layout, migrating
// legacy SHA-256 entries from the data dir root when SHA-256 is among the
// functions. Data under other functions' roots is left untouched. It returns
// the per-function root paths.
func EnsureFunctionRoots(dataDir string, fns []digest.Function, sharded bool) (map[digest.Function]string, error) {
	roots := make(map[digest.Function]string, len(fns))
	for _, fn := range fns {
		root := filepath.Join(dataDir, fn.String())
		if err := os.Mkdir(root, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if fn == digest.SHA256 {
			// Older layouts stored SHA-256 entries at the data dir root.
			if _, err := MigrateLegacySHA256(dataDir, root, fn.HexLen()); err != nil {
				return nil, fmt.Errorf("migrating legacy sha256 entries: %w", err)
			}
		}
		if _, err := EnsureLayout(root, sharded, fn.HexLen()); err != nil {
			return nil, fmt.Errorf("ensuring directory layout for %s: %w", fn, err)
		}
		roots[fn] = root
	}
	return roots, nil
}

// EnsureLayout creates the necessary directory structure for a hash-addressed
// storage. Existing blobs (file names of exactly hexLen hex chars) are migrated
// without data loss. Returns the number of blobs moved during migration (if any).
func EnsureLayout(dataDir string, sharded bool, hexLen int) (moved int, err error) {
	var layout string
	if sharded {
		moved, err = ensureSharded(dataDir, hexLen)
		layout = "sharded"
	} else {
		moved, err = ensureFlat(dataDir, hexLen)
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

// renameBlob moves a blob file from src to dst, replacing an existing dst
// (whose content is stale from an interrupted migration or, by
// content-addressing, identical). On POSIX os.Rename replaces dst atomically;
// on Windows it fails with ErrExist, so dst is removed first.
func renameBlob(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := os.Remove(dst); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

// ensureSharded creates {00..ff} shard directories and moves any top-level
// blob files into their corresponding shard.
func ensureSharded(dataDir string, hexLen int) (int, error) {
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
		if e.IsDir() || !looksLikeBlobName(name, hexLen) {
			continue
		}

		src := filepath.Join(dataDir, name)
		dst := filepath.Join(dataDir, name[:2], name)
		if err := renameBlob(src, dst); err != nil {
			return moved, err
		}
		moved++
	}

	return moved, nil
}

// ensureFlat moves blobs out of any existing shard directories up into dataDir
// and removes the now-empty shard dirs. It probes the 256 possible shard-dir
// names instead of listing dataDir, which can hold hundreds of thousands of
// blobs in the flat layout.
func ensureFlat(dataDir string, hexLen int) (int, error) {
	var moved int

	for i := range 256 {
		shardDir := filepath.Join(dataDir, fmt.Sprintf("%02x", i))
		fi, err := os.Lstat(shardDir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return moved, err
		}
		if !fi.IsDir() {
			continue
		}

		blobs, err := os.ReadDir(shardDir)
		if err != nil {
			return moved, err
		}

		for _, b := range blobs {
			if b.IsDir() || !looksLikeBlobName(b.Name(), hexLen) {
				continue
			}
			src := filepath.Join(shardDir, b.Name())
			dst := filepath.Join(dataDir, b.Name())
			if err := renameBlob(src, dst); err != nil {
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

// MigrateLegacySHA256 moves SHA-256 entries from the legacy layout, which
// stored them at the data dir root (flat, or in {00..ff} shard directories),
// into the keyed subdirectory fnRoot. Only entries that belong to the legacy
// layout are touched: blob files of exactly hexLen hex chars and two-hex-char
// shard directories (function subdirectories and the tmp dir have longer
// names). Shard directories are renamed wholesale when possible, falling back
// to per-blob moves into an existing target (e.g. after an interrupted
// migration). The flat/sharded layout inside fnRoot is normalized by the
// EnsureLayout call that follows. Returns the number of top-level entries
// relocated.
func MigrateLegacySHA256(dataDir, fnRoot string, hexLen int) (moved int, err error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case !e.IsDir() && looksLikeBlobName(name, hexLen):
			if err := renameBlob(filepath.Join(dataDir, name), filepath.Join(fnRoot, name)); err != nil {
				return moved, err
			}
			moved++
		case e.IsDir() && LooksLikeShardDir(name):
			shardMoved, err := moveShardDir(filepath.Join(dataDir, name), filepath.Join(fnRoot, name), hexLen)
			if err != nil {
				return moved, err
			}
			if shardMoved {
				moved++
			}
		}
	}
	if moved > 0 {
		slog.Info("migrated legacy sha256 entries into keyed directory", "path", fnRoot, "moved_entries", moved)
	}
	return moved, nil
}

// moveShardDir moves a legacy shard directory into the keyed root: a whole-
// directory rename when the target does not exist yet, otherwise blob-by-blob
// into the existing target directory. It reports whether anything was moved.
func moveShardDir(src, dst string, hexLen int) (moved bool, err error) {
	if err := os.Rename(src, dst); err == nil {
		return true, nil
	}
	blobs, err := os.ReadDir(src)
	if err != nil {
		return false, err
	}
	for _, b := range blobs {
		if b.IsDir() || !looksLikeBlobName(b.Name(), hexLen) {
			continue
		}
		// A blob already present at the destination is replaced; content is
		// identical by content-addressing.
		if err := renameBlob(filepath.Join(src, b.Name()), filepath.Join(dst, b.Name())); err != nil {
			return moved, err
		}
		moved = true
	}
	// Remove the now-empty shard dir. A non-empty dir (unexpected contents)
	// is left in place and only warned about here: it sits at the data dir
	// root, which validate() never walks.
	if err := os.Remove(src); err != nil {
		slog.Warn("leftover legacy shard directory could not be removed", "path", src, "error", err)
	}
	return moved, nil
}

// LooksLikeShardDir reports whether name is a two-hex-char shard directory
// name ({00..ff}). The legacy-migration and validation walks rely on this to
// tell shard directories apart from per-function roots, so no digest function
// may have a two-character name (asserted by TestRegistryConsistency in
// hashigo/digest).
func LooksLikeShardDir(name string) bool {
	return len(name) == 2 && digest.IsHex(name[0]) && digest.IsHex(name[1])
}

// SkipStrayFile handles a file encountered during a validation walk whose name
// is not a hexLen-char digest: leftover "tmp_*" files from a crashed write are
// deleted, anything else is logged and left in place out of caution. It
// reports whether the file was such a stray.
func SkipStrayFile(path, name string, hexLen int) (stray bool, err error) {
	if len(name) == hexLen {
		return false, nil
	}
	if strings.HasPrefix(name, "tmp_") {
		// These might be leftover from a previous crash and are safe to delete.
		slog.Warn("deleting leftover temporary file", "path", path)
		return true, os.Remove(path)
	}
	slog.Warn("ignoring file with unexpected name", "path", path)
	return true, nil
}

func looksLikeBlobName(name string, hexLen int) bool {
	if len(name) != hexLen {
		return false
	}
	for i := range len(name) {
		if !digest.IsHex(name[i]) {
			return false
		}
	}
	return true
}
