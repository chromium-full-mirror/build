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
	errpb "google.golang.org/genproto/googleapis/rpc/errdetails"
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
	// Recursively materialize all directories in the sandbox.
	action.InputTrie.Root().Walk(func(k []byte, dir *model.KajiyaDirectory) bool {
		dirPath := filepath.Join(sb.sandboxDir, string(k))
		if err = MaterializeDirectory(sb.cas, dirPath, dir, true); err != nil {
			return true
		}
		return false
	})

	return err
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
	var violations []*errpb.PreconditionFailure_Violation
	action.InputTrie.Root().Walk(func(k []byte, dir *model.KajiyaDirectory) bool {
		for _, outputPath := range dir.Outputs {
			var fullPath, pathFromWorkDir string
			fullPath = filepath.Join(sb.sandboxDir, string(k), outputPath.Name)
			pathFromWorkDir, err = filepath.Rel(action.WorkingDir, filepath.Join(string(k), outputPath.Name))
			if err != nil {
				log.Printf("🚨 failed to get relative path: %v", err)
				return true
			}

			var lfi os.FileInfo
			lfi, err = os.Lstat(fullPath)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					// Ignore non-existing output files.
					log.Printf("🚨 output file %q does not exist, ignoring", fullPath)
					continue
				}
				return true
			}

			if lfi.Mode()&os.ModeSymlink != 0 {
				var fi os.FileInfo
				fi, err = os.Stat(fullPath)
				if err != nil {
					// Ignore dangling symlinks.
					log.Printf("🚨 output file %q is a dangling symlink, ignoring", fullPath)
					continue
				}
				if fi.Mode().IsDir() {
					switch outputPath.Type {
					case model.File:
						violations = append(violations, &errpb.PreconditionFailure_Violation{
							Type:        "INVALID_ARGUMENT",
							Subject:     pathFromWorkDir,
							Description: fmt.Sprintf("output %q links to directory, but was expected to link to a file", pathFromWorkDir),
						})
						continue
					case model.Directory:
						actionResult.OutputDirectorySymlinks = append(actionResult.OutputDirectorySymlinks, &repb.OutputSymlink{ //nolint:staticcheck
							Path:   pathFromWorkDir,
							Target: fi.Name(),
						})
					case model.Unknown:
						actionResult.OutputSymlinks = append(actionResult.OutputSymlinks, &repb.OutputSymlink{
							Path:   pathFromWorkDir,
							Target: fi.Name(),
						})
					}
				} else if fi.Mode().IsRegular() {
					switch outputPath.Type {
					case model.File:
						actionResult.OutputFileSymlinks = append(actionResult.OutputFileSymlinks, &repb.OutputSymlink{ //nolint:staticcheck
							Path:   pathFromWorkDir,
							Target: fi.Name(),
						})
					case model.Directory:
						violations = append(violations, &errpb.PreconditionFailure_Violation{
							Type:        "INVALID_ARGUMENT",
							Subject:     pathFromWorkDir,
							Description: fmt.Sprintf("output %q links to a file, but was expected to link to a directory", pathFromWorkDir),
						})
						continue
					case model.Unknown:
						actionResult.OutputSymlinks = append(actionResult.OutputSymlinks, &repb.OutputSymlink{
							Path:   pathFromWorkDir,
							Target: fi.Name(),
						})
					}
				} else {
					violations = append(violations, &errpb.PreconditionFailure_Violation{
						Type:        "INVALID_ARGUMENT",
						Subject:     pathFromWorkDir,
						Description: fmt.Sprintf("output %q links to a special file (mode: %q), which is not supported", pathFromWorkDir, fi.Mode().String()),
					})
					continue
				}
			} else if lfi.IsDir() {
				if outputPath.Type == model.File {
					violations = append(violations, &errpb.PreconditionFailure_Violation{
						Type:        "INVALID_ARGUMENT",
						Subject:     pathFromWorkDir,
						Description: fmt.Sprintf("output %q is a directory, but was expected to be a file", pathFromWorkDir),
					})
					continue
				}

				// Upload the directory to the CAS.
				var dirs []*repb.Directory
				dirs, err = sb.buildMerkleTree(fullPath)
				if err != nil {
					return true
				}

				tree := repb.Tree{
					Root: dirs[0],
				}
				if len(dirs) > 1 {
					tree.Children = dirs[1:]
				}

				var treeBytes []byte
				treeBytes, err = proto.Marshal(&tree)
				if err != nil {
					return true
				}

				var d digest.Digest
				d, err = sb.cas.Put(treeBytes)
				if err != nil {
					return true
				}

				actionResult.OutputDirectories = append(actionResult.OutputDirectories, &repb.OutputDirectory{
					Path:                  pathFromWorkDir,
					TreeDigest:            d.ToProto(),
					IsTopologicallySorted: false,
				})
			} else if lfi.Mode().IsRegular() {
				if outputPath.Type == model.Directory {
					violations = append(violations, &errpb.PreconditionFailure_Violation{
						Type:        "INVALID_ARGUMENT",
						Subject:     pathFromWorkDir,
						Description: fmt.Sprintf("output %q is a file, but was expected to be a directory", pathFromWorkDir),
					})
					continue
				}

				// Upload the file to the CAS.
				var d digest.Digest
				d, err = digest.NewFromFile(fullPath)
				if err != nil {
					return true
				}
				if err = sb.cas.Adopt(d, fullPath); err != nil {
					return true
				}

				actionResult.OutputFiles = append(actionResult.OutputFiles, &repb.OutputFile{
					Path:         pathFromWorkDir,
					Digest:       d.ToProto(),
					IsExecutable: lfi.Mode()&0111 != 0,
				})
			} else {
				violations = append(violations, &errpb.PreconditionFailure_Violation{
					Type:        "INVALID_ARGUMENT",
					Subject:     pathFromWorkDir,
					Description: fmt.Sprintf("output %q is a special file (mode: %q), which is not supported", pathFromWorkDir, lfi.Mode().String()),
				})
				continue
			}
		}
		return false
	})
	return err
}
