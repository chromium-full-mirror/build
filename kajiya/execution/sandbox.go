// Copyright 2025 The Chromium Authors
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/model"
)

// Sandbox manages the sandbox environment for an action.
type Sandbox struct {
	// The CAS to use.
	cas   *blobstore.ContentAddressableStorage
	trees *TreeRepository

	// The directory of the sandbox.
	sandboxDir string
}

// Prepare ensures that all necessary directories and files for the given action are present in the
// sandbox.
func (sb *Sandbox) Prepare(action *model.Action) (err error) {
	// Stage the input files and directories into the sandbox.
	if err := sb.trees.StageDirectory(action.InputRootDigest, sb.sandboxDir); err != nil {
		return fmt.Errorf("failed to materialize input root: %w", err)
	}

	// Verify that the working directory exists. REAPI requires that the working directory
	// is part of the input root.
	workDir := filepath.Join(sb.sandboxDir, action.WorkingDir)
	if _, err := os.Stat(workDir); err != nil {
		return status.Errorf(codes.FailedPrecondition, "working directory %q is not an input directory: %v", action.WorkingDir, err)
	}

	// In contrast to the working directory, REAPI does not require that the parent directories
	// of output paths are part of the input root, so we need to create them ourselves.
	for _, outputPath := range action.OutputPaths {
		if err := os.MkdirAll(filepath.Join(workDir, filepath.Dir(outputPath)), 0755); err != nil {
			return fmt.Errorf("failed to create parent directories for output path %q: %w", outputPath, err)
		}
	}

	return nil
}

// buildMerkleTree recursively walks through the given directory and builds a
// merkle tree of the directory. The returned slice contains the root directory
// and all subdirectories.
func (sb *Sandbox) buildMerkleTree(path string) ([]*repb.Directory, error) {
	dir := &repb.Directory{}
	dirs := []*repb.Directory{}

	dirEntries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	for _, dirEntry := range dirEntries {
		// TODO: Add symlink support?
		if dirEntry.IsDir() {
			subDirs, err := sb.buildMerkleTree(filepath.Join(path, dirEntry.Name()))
			if err != nil {
				return nil, fmt.Errorf("failed to build merkle tree: %w", err)
			}
			d, err := digest.NewFromMessage(subDirs[0])
			if err != nil {
				return nil, fmt.Errorf("failed to get digest: %w", err)
			}
			dir.Directories = append(dir.Directories, &repb.DirectoryNode{
				Name:   dirEntry.Name(),
				Digest: d.ToProto(),
			})
			dirs = append(dirs, subDirs...)
		} else {
			d, err := digest.NewFromFile(filepath.Join(path, dirEntry.Name()))
			if err != nil {
				return nil, fmt.Errorf("failed to get digest: %w", err)
			}
			fi, err := dirEntry.Info()
			if err != nil {
				return nil, fmt.Errorf("failed to get file info: %w", err)
			}
			fileNode := &repb.FileNode{
				Name:         dirEntry.Name(),
				Digest:       d.ToProto(),
				IsExecutable: fi.Mode()&0111 != 0,
			}
			err = sb.cas.Adopt(d, filepath.Join(path, dirEntry.Name()))
			if err != nil {
				return nil, fmt.Errorf("failed to move file into CAS: %w", err)
			}
			dir.Files = append(dir.Files, fileNode)
		}
	}

	dirBytes, err := proto.Marshal(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal directory: %w", err)
	}
	if _, err = sb.cas.Put(dirBytes); err != nil {
		return nil, err
	}

	return append([]*repb.Directory{dir}, dirs...), nil
}

// UploadOutputs moves all outputs declared in the Command into the CAS and updates the
// OutputDirectories and OutputFiles attributes of the actionResult with metadata about them.
func (sb *Sandbox) UploadOutputs(action *model.Action, actionResult *repb.ActionResult) (err error) {
	workDir := filepath.Join(sb.sandboxDir, action.WorkingDir)
	for _, outputPath := range action.OutputPaths {
		joinedPath := filepath.Join(workDir, outputPath)
		fi, err := os.Stat(joinedPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Ignore non-existing output files.
				log.Printf("🚨 output file %q does not exist, ignoring", joinedPath)
				continue
			}
			return fmt.Errorf("failed to stat output path %q: %w", outputPath, err)
		}
		if fi.IsDir() {
			// Upload the directory to the CAS.
			dirs, err := sb.buildMerkleTree(joinedPath)
			if err != nil {
				return fmt.Errorf("failed to build merkle tree for %q: %w", outputPath, err)
			}

			tree := repb.Tree{
				Root: dirs[0],
			}
			if len(dirs) > 1 {
				tree.Children = dirs[1:]
			}
			treeBytes, err := proto.Marshal(&tree)
			if err != nil {
				return fmt.Errorf("failed to marshal tree: %w", err)
			}
			d, err := sb.cas.Put(treeBytes)
			if err != nil {
				return fmt.Errorf("failed to upload tree to CAS: %w", err)
			}

			actionResult.OutputDirectories = append(actionResult.OutputDirectories, &repb.OutputDirectory{
				Path:                  outputPath,
				TreeDigest:            d.ToProto(),
				IsTopologicallySorted: false,
			})
		} else {
			// Upload the file to the CAS.
			d, err := digest.NewFromFile(joinedPath)
			if err != nil {
				return fmt.Errorf("failed to compute digest of file %q: %w", outputPath, err)
			}
			if err := sb.cas.Adopt(d, joinedPath); err != nil {
				return fmt.Errorf("failed to upload file %q to CAS: %w", outputPath, err)
			}

			actionResult.OutputFiles = append(actionResult.OutputFiles, &repb.OutputFile{
				Path:         outputPath,
				Digest:       d.ToProto(),
				IsExecutable: fi.Mode()&0111 != 0,
			})
		}
	}
	return nil
}
