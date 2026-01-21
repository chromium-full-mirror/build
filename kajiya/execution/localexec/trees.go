// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package localexec

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sync/singleflight"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/digest"
	"go.chromium.org/build/kajiya/execution/model"
)

// TreeRepository is a repository for trees. It provides methods for materializing trees in the
// local filesystem, which can then be mounted into an action's input root later.
type TreeRepository struct {
	// Base directory for all trees
	baseDir string

	// The CAS to use for fetching directory protos and files.
	cas *blobstore.ContentAddressableStorage

	// Synchronization mechanism to prevent multiple concurrent materializations of the same directory.
	materializeSyncer singleflight.Group
}

func newTreeRepository(baseDir string, cas *blobstore.ContentAddressableStorage) (*TreeRepository, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("baseDir must not be empty")
	}

	if err := os.Mkdir(baseDir, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("failed to create directory %q: %w", baseDir, err)
	}

	// Create all shard subdirectories.
	for i := range 256 {
		shardDir := filepath.Join(baseDir, fmt.Sprintf("%02x", i))
		if err := os.Mkdir(shardDir, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("failed to create directory %q: %w", shardDir, err)
		}
	}

	return &TreeRepository{
		baseDir: baseDir,
		cas:     cas,
	}, nil
}

// Path returns the path on disk where the directory with the given digest is materialized.
func (t *TreeRepository) Path(dirDigest digest.Digest) string {
	return filepath.Join(t.baseDir, dirDigest.Hash[:2], dirDigest.Hash)
}

// EnsureDirectory ensures that the *repb.Directory for a given digest.Digest, is present in the
// tree repository, including all its subdirectories, materializing them first if necessary.
func (t *TreeRepository) EnsureDirectory(dirTrie *model.DirectoryTrie) (err error) {
	dirTrie.Root().Walk(func(k []byte, kd *model.KajiyaDirectory) bool {
		// Check if we already have the directory materialized on disk.
		// If yes, we trust its contents and reuse it, instead of materializing it again.
		// We still need to check whether all subdirectories are there, too, though.
		repoPath := t.Path(kd.Digest)
		if _, err = os.Stat(repoPath); err == nil {
			return false
		}

		var tmpPath string
		_, err, _ = t.materializeSyncer.Do(kd.Digest.Hash, func() (_ any, err error) {
			// Check again if the directory is already materialized.
			if _, err = os.Stat(repoPath); err == nil {
				return nil, nil
			}

			// We need to actually materialize the directory on disk. We do this in a temporary
			// directory first, and then move it to its final location once we're done.
			tmpPath, err = os.MkdirTemp(t.baseDir, "*")
			if err != nil {
				return nil, fmt.Errorf("failed to create temporary directory: %w", err)
			}

			// Materialize the directory itself.
			if err = MaterializeDirectory(t.cas, tmpPath, kd, false); err != nil {
				return nil, fmt.Errorf("failed to materialize directory: %w", err)
			}

			// Move the directory to its final location.
			if err := os.Rename(tmpPath, repoPath); err != nil {
				return nil, fmt.Errorf("failed to rename directory: %w", err)
			}

			return nil, nil
		})
		if err != nil && tmpPath != "" {
			if err := os.RemoveAll(tmpPath); err != nil {
				slog.Error("failed to remove temporary directory", "path", tmpPath, "error", err)
			}
		}

		return false
	})

	return err
}
