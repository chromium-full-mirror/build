// Copyright 2025 The Chromium Authors
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

	errpb "google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/proto"

	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/digest"
	"go.chromium.org/build/kajiya/execution/model"
)

// SandboxStrategy is an enum that defines the different sandbox strategies that can be used.
type SandboxStrategy int

const (
	// Files creates a sandbox directory for the input root. Each input file is copied into the
	// sandbox via clonefile (on macOS) or hardlink (on other OS). There is no protection
	// against modification of the input files. This strategy requires O(n+m) time with
	// n = number of input directories, m = number of input files.
	Files SandboxStrategy = iota

	// OverlayFS is similar to Files, but Kajiya will mount an overlayfs filesystem over the
	// sandbox to redirect any writes into a separate "upper" directory. This ensures that the
	// action cannot modify any files in the CAS.
	OverlayFS

	// NestedOverlayFS is an experimental strategy that materializes each directory of the input
	// root only once (even across actions) inside a tree repository and reuses them across
	// actions. It then mounts each input directory from the repository into the location inside
	// the sandbox as a separate overlayfs. This reduces the time needed for building the
	// sandbox to amortized O(n), with n = number of input directories, under the assumption
	// that most actions share the majority of their input dirs.
	NestedOverlayFS

	// FuseFS mounts a FUSE filesystem for the lifetime of the Kajiya process that serves
	// CAS files on demand as a virtual directory tree. For each action, the input tree is
	// registered as a dynamic subtree in the FUSE mount (no file materialization at all).
	// Overlayfs is mounted on top for write redirection, same as the OverlayFS strategy.
	FuseFS
)

// Sandbox manages the sandbox environment for an action.
type Sandbox struct {
	// The CAS to use.
	cas   *blobstore.ContentAddressableStorage
	trees *TreeRepository

	// The directory of the sandbox.
	sandboxDir string

	// Whether to use overlayfs for the sandbox.
	strategy        SandboxStrategy
	overlayLowerDir string
	overlayUpperDir string
	overlayWorkDir  string
	extraNsjailArgs []string

	// FuseFS-specific machinery. Nil for non-FUSE strategies.
	fuse          *fuseBackend
	fuseSandboxID string

	// Input tracing recorder. When non-nil, file opens through the FUSE
	// layer are recorded for reporting as auxiliary metadata.
	recorder *AccessRecorder
}

// Id returns a unique identifier for the mount point of a directory. This is used to construct
// mount paths in the overlay filesystem. We use the address of the directory as the identifier,
// because it's unique, cheap to compute, deterministic, and short enough to be used as a filename.
// It is not visible to the action itself, and the sandbox is deleted after each action, so it
// doesn't matter that these IDs aren't stable across Kajiya invocations.
func mountID(dir *model.KajiyaDirectory) string {
	// Drop the "0x". It's cleaner.
	return fmt.Sprintf("%p", dir)[2:]
}

// Prepare creates the directories and files in the sandbox for the given DirectoryTrie.
func (sb *Sandbox) Prepare(action *model.Action) (err error) {
	switch sb.strategy {
	case NestedOverlayFS:
		// Ensure that we have all directories required to build our sandbox.
		if err = sb.trees.EnsureDirectory(action.InputTrie); err != nil {
			return err
		}
		fallthrough
	case OverlayFS:
		// Create the lower, upper, work directories required by overlayfs.
		sb.overlayLowerDir = filepath.Join(sb.sandboxDir, "lower")
		if err = os.Mkdir(sb.overlayLowerDir, 0755); err != nil {
			return fmt.Errorf("failed to create overlay lower directory: %w", err)
		}
		sb.overlayUpperDir = filepath.Join(sb.sandboxDir, "upper")
		if err = os.Mkdir(sb.overlayUpperDir, 0755); err != nil {
			return fmt.Errorf("failed to create overlay upper directory: %w", err)
		}
		sb.overlayWorkDir = filepath.Join(sb.sandboxDir, "work")
		if err = os.Mkdir(sb.overlayWorkDir, 0755); err != nil {
			return fmt.Errorf("failed to create overlay work directory: %w", err)
		}
	case FuseFS:
		// Create upper and work directories for overlayfs. The lower directory
		// is served by the FUSE filesystem, so no materialization is needed.
		sb.overlayUpperDir = filepath.Join(sb.sandboxDir, "upper")
		if err = os.Mkdir(sb.overlayUpperDir, 0755); err != nil {
			return fmt.Errorf("failed to create overlay upper directory: %w", err)
		}
		sb.overlayWorkDir = filepath.Join(sb.sandboxDir, "work")
		if err = os.Mkdir(sb.overlayWorkDir, 0755); err != nil {
			return fmt.Errorf("failed to create overlay work directory: %w", err)
		}

		// Register the input tree with the FUSE filesystem. This creates the
		// entire directory tree as virtual inodes backed by CAS -- no files
		// are materialized on disk.
		sb.fuseSandboxID = filepath.Base(sb.sandboxDir)
		sb.overlayLowerDir, err = sb.fuse.RegisterSandbox(
			sb.fuseSandboxID, action.InputTrie, sb.cas, sb.recorder)
		if err != nil {
			return fmt.Errorf("failed to register sandbox with FUSE: %w", err)
		}

		// Create output parent directories in the overlayfs upper layer.
		// The FUSE lower layer only serves immutable CAS content; output
		// directory structure is per-action and belongs in the upper layer.
		action.InputTrie.Root().Walk(func(k []byte, dir *model.KajiyaDirectory) bool {
			if len(dir.Outputs) == 0 {
				return false
			}
			dirPath := filepath.Join(sb.overlayUpperDir, string(k))
			if err = os.MkdirAll(dirPath, 0755); err != nil {
				err = fmt.Errorf("failed to create output parent in upper layer: %w", err)
				return true
			}
			if err = CreateOutputDirectories(dirPath, dir); err != nil {
				return true
			}
			return false
		})
		if err != nil {
			return err
		}

	}

	// FuseFS handles the entire input tree via FUSE inodes, so we skip
	// the materialization walk.
	if sb.strategy != FuseFS {
		action.InputTrie.Root().Walk(func(k []byte, dir *model.KajiyaDirectory) bool {
			// Create the directory in the sandbox.
			switch sb.strategy {
			case Files:
				dirPath := filepath.Join(sb.sandboxDir, string(k))
				if err = MaterializeDirectory(sb.cas, dirPath, dir, true); err != nil {
					return true
				}
			case OverlayFS:
				dirPath := filepath.Join(sb.overlayLowerDir, string(k))
				if err = MaterializeDirectory(sb.cas, dirPath, dir, true); err != nil {
					return true
				}
			case NestedOverlayFS:
				mntTarget := filepath.Join("/mnt", string(k))
				mntLowerDir := sb.trees.Path(dir.Digest)
				mntUpperDir := filepath.Join(sb.overlayUpperDir, mountID(dir))
				if err = os.Mkdir(mntUpperDir, 0755); err != nil {
					return true
				}
				mntWorkDir := filepath.Join(sb.overlayWorkDir, mountID(dir))
				if err = os.Mkdir(mntWorkDir, 0755); err != nil {
					return true
				}

				sb.extraNsjailArgs = append(
					sb.extraNsjailArgs,
					"--mount",
					fmt.Sprintf("overlay:%s:overlay:lowerdir=%s,upperdir=%s,workdir=%s,userxattr,index=off,xino=off,volatile",
						mntTarget,
						mntLowerDir,
						mntUpperDir,
						mntWorkDir,
					),
				)
			}

			return false
		})

		if err != nil {
			return err
		}
	}

	switch sb.strategy {
	case Files:
		sb.extraNsjailArgs = append(
			sb.extraNsjailArgs,
			"--bindmount",
			fmt.Sprintf("%s:%s", sb.sandboxDir, "/mnt"),
		)
	case OverlayFS, FuseFS:
		sb.extraNsjailArgs = append(
			sb.extraNsjailArgs,
			"--mount",
			fmt.Sprintf("overlay:/mnt:overlay:lowerdir=%s,upperdir=%s,workdir=%s,userxattr,index=off,xino=off,volatile",
				sb.overlayLowerDir,
				sb.overlayUpperDir,
				sb.overlayWorkDir,
			),
		)
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
			d, err := digest.FromMessage(subDirs[0])
			if err != nil {
				return nil, fmt.Errorf("failed to get digest: %w", err)
			}
			dir.Directories = append(dir.Directories, &repb.DirectoryNode{
				Name:   dirEntry.Name(),
				Digest: d.ToProto(),
			})
			dirs = append(dirs, subDirs...)
		} else {
			d, err := digest.FromFile(filepath.Join(path, dirEntry.Name()))
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
			switch sb.strategy {
			case Files:
				fullPath = filepath.Join(sb.sandboxDir, string(k), outputPath.Name)
			case OverlayFS, FuseFS:
				fullPath = filepath.Join(sb.overlayUpperDir, string(k), outputPath.Name)
			case NestedOverlayFS:
				fullPath = filepath.Join(sb.overlayUpperDir, mountID(dir), outputPath.Name)
			}
			pathFromWorkDir, err = filepath.Rel(action.WorkingDir, filepath.Join(string(k), outputPath.Name))
			if err != nil {
				slog.Error("failed to get relative path", "error", err)
				return true
			}

			var lfi os.FileInfo
			lfi, err = os.Lstat(fullPath)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					// Ignore non-existing output files. Suppress the log for
					// clang-crashreports, which is almost never present.
					if filepath.Base(outputPath.Name) != "clang-crashreports" {
						slog.Warn("ignoring missing output", "path", fullPath)
					}
					err = nil
					continue
				}
				return true
			}

			if lfi.Mode()&os.ModeSymlink != 0 {
				var fi os.FileInfo
				fi, err = os.Stat(fullPath)
				if err != nil {
					// Ignore dangling symlinks.
					slog.Warn("ignoring dangling symlink output", "path", fullPath)
					err = nil
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
				d, err = digest.FromFile(fullPath)
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
