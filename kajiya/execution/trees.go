// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execution

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"golang.org/x/sync/singleflight"

	"infra/build/kajiya/blobstore"
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

// treeNode represents a node in a directory merkle tree.
type treeNode struct {
	// The digest of the directory that contains the files and directories of this tree node.
	Digest string

	// The relative path in the sandbox of this node.
	Path string

	// Subdirectories inside this directory.
	Children []*treeNode
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

// StageDirectory downloads the given directory from the CAS and writes it to
// the given path. If the directory already exists, it is overwritten.
func (t *TreeRepository) StageDirectory(dirDigest *repb.Digest, path string) ([]digest.Digest, error) {
	// First, ensure that the directory tree has been materialized.
	root, missingBlobs, err := t.materializeDirectory(dirDigest, "")
	if err != nil {
		return nil, fmt.Errorf("failed to materialize directory: %w", err)
	}
	if len(missingBlobs) > 0 {
		return missingBlobs, nil
	}

	// Traverse the tree and copy files and directories.
	stack := []*treeNode{root}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		// Copy the directory from our tree repository to the destination.
		sourceDir := filepath.Join(t.baseDir, node.Digest)
		destDir := filepath.Join(path, node.Path)

		// Copy all files from the source directory to the destination directory.
		dentries, err := os.ReadDir(sourceDir)
		if err != nil {
			return nil, fmt.Errorf("failed to read directory: %w", err)
		}
		for _, d := range dentries {
			srcPath := filepath.Join(sourceDir, d.Name())
			destPath := filepath.Join(destDir, d.Name())
			if d.IsDir() {
				if err := os.Mkdir(destPath, 0755); err != nil {
					return nil, fmt.Errorf("failed to create directory: %w", err)
				}
			} else {
				if err := blobstore.FastCopy(srcPath, destPath); err != nil {
					return nil, fmt.Errorf("failed to create hardlink: %w", err)
				}
			}
		}

		// Add all children to the stack.
		stack = append(stack, node.Children...)
	}

	return nil, nil
}

// materializeDirectory recursively materializes the given directory in the tree repository.
func (t *TreeRepository) materializeDirectory(dirDigest *repb.Digest, nodePath string) (*treeNode, []digest.Digest, error) {
	var missingBlobs []digest.Digest

	// Get the directory message from the CAS.
	d := &repb.Directory{}
	if err := t.cas.Proto(dirDigest, d); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			missingBlobs = append(missingBlobs, digest.NewFromProtoUnvalidated(dirDigest))
			return nil, missingBlobs, nil
		}
		return nil, missingBlobs, fmt.Errorf("failed to fetch directory proto: %w", err)
	}

	// Check if we already have the directory materialized on disk.
	path := filepath.Join(t.baseDir, dirDigest.Hash)
	if _, err := os.Stat(path); err == nil {
		node := &treeNode{
			Digest: dirDigest.Hash,
			Path:   nodePath,
		}

		// If yes, we trust its contents and reuse it, instead of materializing it again.
		// We still need to check whether all subdirectories are there, too, though.
		for _, sd := range d.Directories {
			sdNode, sdMissingBlobs, err := t.materializeDirectory(sd.Digest, filepath.Join(nodePath, sd.Name))
			if err != nil {
				return nil, missingBlobs, fmt.Errorf("failed to materialize subdirectory: %w", err)
			}
			node.Children = append(node.Children, sdNode)
			missingBlobs = append(missingBlobs, sdMissingBlobs...)
		}
		if len(missingBlobs) > 0 {
			return nil, missingBlobs, nil
		}
		return node, nil, nil
	}

	// If we get to this point, it means that we actually have to materialize
	// the directory on disk. We do this in a temporary directory first, and
	// then move it to its final location once we're done.
	node, err, _ := t.materializeSyncer.Do(dirDigest.Hash, func() (any, error) {
		node := &treeNode{
			Digest: dirDigest.Hash,
			Path:   nodePath,
		}

		tmpPath, err := os.MkdirTemp(t.baseDir, "*")
		if err != nil {
			return nil, fmt.Errorf("failed to create temporary directory: %w", err)
		}
		defer func() {
			if tmpPath != "" {
				if err := os.RemoveAll(tmpPath); err != nil {
					log.Printf("🚨 failed to remove temporary directory: %v", err)
				}
			}
		}()

		// First, materialize all the input files and symlinks in the directory.
		for _, f := range d.Files {
			filePath := filepath.Join(tmpPath, f.Name)
			if err = t.materializeFile(filePath, f); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					missingBlobs = append(missingBlobs, digest.NewFromProtoUnvalidated(f.Digest))
					continue
				}
				return nil, fmt.Errorf("failed to materialize file: %w", err)
			}
		}
		for _, sl := range d.Symlinks {
			slPath := filepath.Join(tmpPath, sl.Name)
			if err = os.Symlink(sl.Target, slPath); err != nil {
				return nil, fmt.Errorf("failed to create symlink: %w", err)
			}
		}

		// Materialize all the subdirectories.
		for _, sd := range d.Directories {
			sdNode, sdMissingBlobs, err := t.materializeDirectory(sd.Digest, filepath.Join(nodePath, sd.Name))
			if err != nil {
				return nil, fmt.Errorf("failed to materialize subdirectory: %w", err)
			}
			node.Children = append(node.Children, sdNode)
			missingBlobs = append(missingBlobs, sdMissingBlobs...)
			if err = os.Mkdir(filepath.Join(tmpPath, sd.Name), 0755); err != nil {
				return nil, fmt.Errorf("failed to create directory: %w", err)
			}
		}

		// Finally, set the directory properties. We have to do this after the files have been
		// materialized, because otherwise the mtime of the directory would be updated to the
		// current time.
		if d.NodeProperties != nil {
			if d.NodeProperties.UnixMode != nil {
				if err = os.Chmod(tmpPath, os.FileMode(d.NodeProperties.UnixMode.Value)); err != nil {
					return nil, fmt.Errorf("failed to set mode: %w", err)
				}
			}

			if d.NodeProperties.Mtime != nil {
				time := d.NodeProperties.Mtime.AsTime()
				if err = os.Chtimes(tmpPath, time, time); err != nil {
					return nil, fmt.Errorf("failed to set mtime: %w", err)
				}
			}
		}

		// If any blobs are missing, we report them and discard the (incomplete) directory
		// we just materialized. The caller will have to retry the materialization later.
		if len(missingBlobs) > 0 {
			return nil, nil
		}

		if err = os.Rename(tmpPath, path); err != nil {
			return nil, fmt.Errorf("failed to rename directory: %w", err)
		}

		// If we moved the directory successfully, we don't need to delete it anymore.
		tmpPath = ""
		return node, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to materialize subdirectory: %w", err)
	}
	if len(missingBlobs) > 0 {
		return nil, missingBlobs, nil
	}
	return node.(*treeNode), nil, nil
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
