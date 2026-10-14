// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package localexec

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/go-cmp/cmp"
	iradix "github.com/hashicorp/go-immutable-radix/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/model"
)

// TestUploadOutputsReportsSymlinkTarget checks that a symlink output is
// reported with the target stored in the link, not with the link's own name.
func TestUploadOutputsReportsSymlinkTarget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decl     model.FileType
		target   string
		mkTarget func(t *testing.T, path string)
		symlinks func(ar *repb.ActionResult) []*repb.OutputSymlink
	}{
		{
			name:     "file_link_declared_file",
			decl:     model.File,
			target:   "data/file.txt",
			mkTarget: writeTestFile,
			symlinks: func(ar *repb.ActionResult) []*repb.OutputSymlink { return ar.OutputFileSymlinks }, //nolint:staticcheck
		},
		{
			name:     "file_link_declared_unknown",
			decl:     model.Unknown,
			target:   "data/file.txt",
			mkTarget: writeTestFile,
			symlinks: func(ar *repb.ActionResult) []*repb.OutputSymlink { return ar.OutputSymlinks },
		},
		{
			name:     "dir_link_declared_directory",
			decl:     model.Directory,
			target:   "data/dir",
			mkTarget: makeTestDir,
			symlinks: func(ar *repb.ActionResult) []*repb.OutputSymlink { return ar.OutputDirectorySymlinks }, //nolint:staticcheck
		},
		{
			name:     "dir_link_declared_unknown",
			decl:     model.Unknown,
			target:   "data/dir",
			mkTarget: makeTestDir,
			symlinks: func(ar *repb.ActionResult) []*repb.OutputSymlink { return ar.OutputSymlinks },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Create a Files-strategy sandbox whose root declares one output.
			cas, err := blobstore.New(t.Context(), filepath.Join(t.TempDir(), "cas"))
			if err != nil {
				t.Fatalf("blobstore.New: %v", err)
			}
			sb := &Sandbox{
				cas:        cas,
				digestFn:   digest.SHA256,
				sandboxDir: t.TempDir(),
				strategy:   Files,
			}
			root := &model.KajiyaDirectory{Outputs: []model.KajiyaOutput{{Name: "link", Type: tc.decl}}}
			tx := iradix.New[*model.KajiyaDirectory]().Txn()
			tx.Insert(nil, root)
			action := &model.Action{WorkingDir: ".", InputTrie: tx.Commit()}

			// Make the output a relative symlink to an existing target.
			tc.mkTarget(t, filepath.Join(sb.sandboxDir, tc.target))
			if err := os.Symlink(tc.target, filepath.Join(sb.sandboxDir, "link")); err != nil {
				t.Fatalf("os.Symlink: %v", err)
			}

			// Upload the outputs and check the reported symlink.
			ar := &repb.ActionResult{}
			if err := sb.UploadOutputs(action, ar); err != nil {
				t.Fatalf("UploadOutputs: %v", err)
			}
			links := tc.symlinks(ar)
			if got, want := len(links), 1; got != want {
				t.Fatalf("got %d symlinks, want %d (%v)", got, want, ar)
			}
			if got, want := links[0].GetPath(), "link"; got != want {
				t.Errorf("Path: got %q, want %q", got, want)
			}
			if got, want := links[0].GetTarget(), tc.target; got != want {
				t.Errorf("Target: got %q, want %q", got, want)
			}
		})
	}
}

// writeTestFile creates a regular file at path, including its parents.
func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeTestDir creates a directory at path, including its parents.
func makeTestDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestUploadOutputsRecordsSymlinksInOutputDirectory checks that a symlink
// inside an output directory becomes a SymlinkNode with the target stored in
// the link, whatever that target is, and that the file a link points to keeps
// its mode and content.
func TestUploadOutputsRecordsSymlinksInOutputDirectory(t *testing.T) {
	for _, tc := range []struct {
		name string
		// setup populates the output directory at outDir. Every case
		// also has a file at ../data/file, relative to outDir.
		setup    func(t *testing.T, outDir string)
		wantRoot *repb.Directory
	}{
		{
			// The link sorts before its target, so it is processed first.
			name: "link_to_sibling_file",
			setup: func(t *testing.T, outDir string) {
				writeTestFile(t, filepath.Join(outDir, "b_tool"))
				if err := os.Chmod(filepath.Join(outDir, "b_tool"), 0o755); err != nil {
					t.Fatal(err)
				}
				makeTestSymlink(t, "b_tool", filepath.Join(outDir, "a_link"))
			},
			wantRoot: &repb.Directory{
				Files: []*repb.FileNode{{
					Name:         "b_tool",
					Digest:       digest.SHA256.FromBytes([]byte("content")).Proto(),
					IsExecutable: runtime.GOOS != "windows", // Windows has no executable bit.
				}},
				Symlinks: []*repb.SymlinkNode{{Name: "a_link", Target: "b_tool"}},
			},
		},
		{
			name: "link_outside_output_directory",
			setup: func(t *testing.T, outDir string) {
				makeTestSymlink(t, "../data/file", filepath.Join(outDir, "link"))
			},
			wantRoot: &repb.Directory{
				Symlinks: []*repb.SymlinkNode{{Name: "link", Target: "../data/file"}},
			},
		},
		{
			name: "link_to_directory",
			setup: func(t *testing.T, outDir string) {
				makeTestDir(t, filepath.Join(outDir, "sub"))
				makeTestSymlink(t, "sub", filepath.Join(outDir, "link"))
			},
			wantRoot: &repb.Directory{
				Directories: []*repb.DirectoryNode{{Name: "sub", Digest: digest.SHA256.Empty().Proto()}},
				Symlinks:    []*repb.SymlinkNode{{Name: "link", Target: "sub"}},
			},
		},
		{
			name: "dangling_link",
			setup: func(t *testing.T, outDir string) {
				makeTestSymlink(t, "missing", filepath.Join(outDir, "link"))
			},
			wantRoot: &repb.Directory{
				Symlinks: []*repb.SymlinkNode{{Name: "link", Target: "missing"}},
			},
		},
		{
			name: "links_sorted_by_name",
			setup: func(t *testing.T, outDir string) {
				for _, name := range []string{"c", "a", "b"} {
					makeTestSymlink(t, "../data/file", filepath.Join(outDir, name))
				}
			},
			wantRoot: &repb.Directory{
				Symlinks: []*repb.SymlinkNode{
					{Name: "a", Target: "../data/file"},
					{Name: "b", Target: "../data/file"},
					{Name: "c", Target: "../data/file"},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Create a Files-strategy sandbox whose root declares the
			// output directory "out".
			cas, err := blobstore.New(t.Context(), filepath.Join(t.TempDir(), "cas"))
			if err != nil {
				t.Fatalf("blobstore.New: %v", err)
			}
			sb := &Sandbox{
				cas:        cas,
				digestFn:   digest.SHA256,
				sandboxDir: t.TempDir(),
				strategy:   Files,
			}
			root := &model.KajiyaDirectory{Outputs: []model.KajiyaOutput{{Name: "out", Type: model.Directory}}}
			tx := iradix.New[*model.KajiyaDirectory]().Txn()
			tx.Insert(nil, root)
			action := &model.Action{WorkingDir: ".", InputTrie: tx.Commit()}

			// Create a private file outside the output directory, then
			// populate the output directory.
			outside := filepath.Join(sb.sandboxDir, "data", "file")
			writeTestFile(t, outside)
			if err := os.Chmod(outside, 0o600); err != nil {
				t.Fatal(err)
			}
			outsideInfo, err := os.Stat(outside)
			if err != nil {
				t.Fatalf("os.Stat: %v", err)
			}
			outDir := filepath.Join(sb.sandboxDir, "out")
			makeTestDir(t, outDir)
			tc.setup(t, outDir)

			// Upload the outputs and read back the output directory's Tree.
			ar := &repb.ActionResult{}
			if err := sb.UploadOutputs(action, ar); err != nil {
				t.Fatalf("UploadOutputs: %v", err)
			}
			if got, want := len(ar.OutputDirectories), 1; got != want {
				t.Fatalf("got %d output directories, want %d (%v)", got, want, ar)
			}
			treeDigest, err := digest.SHA256.FromProto(ar.OutputDirectories[0].TreeDigest)
			if err != nil {
				t.Fatalf("FromProto: %v", err)
			}
			treeBytes, err := cas.Get(digest.SHA256, treeDigest)
			if err != nil {
				t.Fatalf("cas.Get: %v", err)
			}
			tree := &repb.Tree{}
			if err := proto.Unmarshal(treeBytes, tree); err != nil {
				t.Fatalf("proto.Unmarshal: %v", err)
			}

			// Check the output directory's entries.
			if diff := cmp.Diff(tc.wantRoot, tree.Root, protocmp.Transform()); diff != "" {
				t.Errorf("Tree.Root mismatch (-want +got):\n%s", diff)
			}

			// Check that the file outside the output directory is untouched.
			fi, err := os.Stat(outside)
			if err != nil {
				t.Fatalf("os.Stat: %v", err)
			}
			if got, want := fi.Mode(), outsideInfo.Mode(); got != want {
				t.Errorf("mode of %s: got %v, want %v", outside, got, want)
			}
			content, err := os.ReadFile(outside)
			if err != nil {
				t.Fatalf("os.ReadFile: %v", err)
			}
			if got, want := string(content), "content"; got != want {
				t.Errorf("content of %s: got %q, want %q", outside, got, want)
			}
		})
	}
}

// makeTestSymlink creates a symlink at path that stores target.
func makeTestSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}
