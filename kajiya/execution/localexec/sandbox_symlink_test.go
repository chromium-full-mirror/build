// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package localexec

import (
	"os"
	"path/filepath"
	"testing"

	iradix "github.com/hashicorp/go-immutable-radix/v2"

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
