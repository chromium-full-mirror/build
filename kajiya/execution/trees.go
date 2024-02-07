// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execution

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"

	"infra/build/kajiya/blobstore"
)

// TreeRepository is a repository for trees. It provides methods for materializing trees in the
// local filesystem, which can then be mounted into an action's input root later.
type TreeRepository struct {
	// Base directory for all trees
	baseDir string

	// The CAS to use for fetching directory protos and files.
	cas *blobstore.ContentAddressableStorage
}

func newTreeRepository(baseDir string, cas *blobstore.ContentAddressableStorage) (*TreeRepository, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("baseDir must not be empty")
	}

	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory %q: %w", baseDir, err)
	}

	return &TreeRepository{
		baseDir: baseDir,
		cas:     cas,
	}, nil
}

// MaterializeDirectory recursively materializes the given directory in the local filesystem. The
// directory itself is created at the given path, and all files and subdirectories are created under
// that path.
func (t *TreeRepository) MaterializeDirectory(path string, d *repb.Directory) (missingBlobs []digest.Digest, err error) {
	// First, materialize all the input files in the directory.
	for _, fileNode := range d.Files {
		filePath := filepath.Join(path, fileNode.Name)
		err = t.materializeFile(filePath, fileNode)
		if err != nil {
			if os.IsNotExist(err) {
				missingBlobs = append(missingBlobs, digest.NewFromProtoUnvalidated(fileNode.Digest))
				continue
			}
			return nil, fmt.Errorf("failed to materialize file: %w", err)
		}
	}

	// Next, materialize all the subdirectories.
	for _, sdNode := range d.Directories {
		sdPath := filepath.Join(path, sdNode.Name)
		err = os.Mkdir(sdPath, 0755)
		if err != nil {
			return nil, fmt.Errorf("failed to create subdirectory: %w", err)
		}

		sd := &repb.Directory{}
		if err = t.cas.Proto(sdNode.Digest, sd); err != nil {
			if os.IsNotExist(err) {
				missingBlobs = append(missingBlobs, digest.NewFromProtoUnvalidated(sdNode.Digest))
				continue
			}
			return nil, fmt.Errorf("failed to get subdirectory: %w", err)
		}

		sdMissingBlobs, err := t.MaterializeDirectory(sdPath, sd)
		missingBlobs = append(missingBlobs, sdMissingBlobs...)
		if err != nil {
			return nil, fmt.Errorf("failed to materialize subdirectory: %w", err)
		}
	}

	// Finally, set the directory properties. We have to do this after the files have been
	// materialized, because otherwise the mtime of the directory would be updated to the
	// current time.
	if d.NodeProperties != nil {
		if d.NodeProperties.Mtime != nil {
			time := d.NodeProperties.Mtime.AsTime()
			if err := os.Chtimes(path, time, time); err != nil {
				return nil, fmt.Errorf("failed to set mtime: %w", err)
			}
		}

		if d.NodeProperties.UnixMode != nil {
			if err := os.Chmod(path, os.FileMode(d.NodeProperties.UnixMode.Value)); err != nil {
				return nil, fmt.Errorf("failed to set mode: %w", err)
			}
		}
	}

	return missingBlobs, nil
}

// materializeFile downloads the given file from the CAS and writes it to the given path.
func (t *TreeRepository) materializeFile(filePath string, fileNode *repb.FileNode) error {
	fileDigest, err := digest.NewFromProto(fileNode.Digest)
	if err != nil {
		return fmt.Errorf("failed to parse file digest: %w", err)
	}

	// Calculate the file permissions from all relevant fields.
	perm := os.FileMode(0644)
	if fileNode.NodeProperties != nil && fileNode.NodeProperties.UnixMode != nil {
		perm = os.FileMode(fileNode.NodeProperties.UnixMode.Value)
	}
	if fileNode.IsExecutable {
		perm |= 0111
	}

	if err := t.cas.LinkTo(fileDigest, filePath); err != nil {
		return fmt.Errorf("failed to link to file in CAS: %w", err)
	}

	if err := os.Chmod(filePath, perm); err != nil {
		return fmt.Errorf("failed to set mode: %w", err)
	}

	if fileNode.NodeProperties != nil && fileNode.NodeProperties.Mtime != nil {
		time := fileNode.NodeProperties.Mtime.AsTime()
		if err := os.Chtimes(filePath, time, time); err != nil {
			return fmt.Errorf("failed to set mtime: %w", err)
		}
	}

	return nil
}
