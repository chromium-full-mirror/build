// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package localexec

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/model"
)

// TestExecuteReportsSymlinkOutputsAsRequested checks that every sandbox
// strategy reports a symlink output with its stored target, in the field
// matching how it was requested, even when the target is missing.
func TestExecuteReportsSymlinkOutputsAsRequested(t *testing.T) {
	skipUnlessNsjailWorks(t)

	// Run each case's script in every strategy, on the input root
	//   in.txt, inlink -> in.txt, a/a.txt, b/b.txt, indir/f.txt
	for _, tc := range []struct {
		name   string
		script string
		// The outputs that the command declares in output_files,
		// output_directories and output_paths.
		files, dirs, paths []string
		// notNested is the reason to skip the case under NestedOverlayFS,
		// or "".
		notNested string
		// The symlinks that the result reports in output_file_symlinks,
		// output_directory_symlinks and output_symlinks.
		wantFileLinks, wantDirLinks, wantLinks []*repb.OutputSymlink
	}{
		{
			name:          "link_to_input_file",
			script:        "ln -s in.txt link",
			files:         []string{"link"},
			wantFileLinks: []*repb.OutputSymlink{{Path: "link", Target: "in.txt"}},
		},
		{
			name:         "link_to_input_directory",
			script:       "ln -s indir link",
			dirs:         []string{"link"},
			wantDirLinks: []*repb.OutputSymlink{{Path: "link", Target: "indir"}},
		},
		{
			name:          "link_to_input_symlink",
			script:        "ln -s inlink link",
			files:         []string{"link"},
			wantFileLinks: []*repb.OutputSymlink{{Path: "link", Target: "inlink"}},
		},
		{
			name:          "output_file_link_to_input_directory",
			script:        "ln -s indir link",
			files:         []string{"link"},
			wantFileLinks: []*repb.OutputSymlink{{Path: "link", Target: "indir"}},
		},
		{
			name:         "output_directory_link_to_input_file",
			script:       "ln -s in.txt link",
			dirs:         []string{"link"},
			wantDirLinks: []*repb.OutputSymlink{{Path: "link", Target: "in.txt"}},
		},
		{
			name:          "link_to_input_in_same_directory",
			script:        "ln -s a.txt a/link",
			files:         []string{"a/link"},
			wantFileLinks: []*repb.OutputSymlink{{Path: "a/link", Target: "a.txt"}},
		},
		{
			name:          "link_to_input_in_parent_directory",
			script:        "ln -s ../in.txt a/link",
			files:         []string{"a/link"},
			wantFileLinks: []*repb.OutputSymlink{{Path: "a/link", Target: "../in.txt"}},
		},
		{
			name:          "link_to_output_in_other_input_directory",
			script:        "echo out >b/out.txt && ln -s ../b/out.txt a/link",
			files:         []string{"a/link"},
			wantFileLinks: []*repb.OutputSymlink{{Path: "a/link", Target: "../b/out.txt"}},
		},
		{
			name:          "link_to_deleted_input",
			script:        "rm in.txt && ln -s in.txt link",
			files:         []string{"link"},
			wantFileLinks: []*repb.OutputSymlink{{Path: "link", Target: "in.txt"}},
		},
		{
			name:          "link_into_recreated_input_directory",
			script:        "rm -r indir && mkdir indir && ln -s indir/f.txt link",
			files:         []string{"link"},
			notNested:     "input directories are mount points, which the action can't remove",
			wantFileLinks: []*repb.OutputSymlink{{Path: "link", Target: "indir/f.txt"}},
		},
		{
			name:          "dangling_link",
			script:        "ln -s missing link",
			files:         []string{"link"},
			wantFileLinks: []*repb.OutputSymlink{{Path: "link", Target: "missing"}},
		},
		{
			name:          "symlink_loop",
			script:        "ln -s link link",
			files:         []string{"link"},
			wantFileLinks: []*repb.OutputSymlink{{Path: "link", Target: "link"}},
		},
		{
			name:      "output_path_link_to_input_file",
			script:    "ln -s in.txt link",
			paths:     []string{"link"},
			wantLinks: []*repb.OutputSymlink{{Path: "link", Target: "in.txt"}},
		},
		{
			name:      "output_path_dangling_link",
			script:    "ln -s missing link",
			paths:     []string{"link"},
			wantLinks: []*repb.OutputSymlink{{Path: "link", Target: "missing"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, strategy := range []struct {
				name string
				s    SandboxStrategy
			}{
				{name: "files", s: Files},
				{name: "overlayfs", s: OverlayFS},
				{name: "nested_overlayfs", s: NestedOverlayFS},
				{name: "fusefs", s: FuseFS},
			} {
				t.Run(strategy.name, func(t *testing.T) {
					// Skip strategies that can't run the case here.
					if strategy.s == NestedOverlayFS && tc.notNested != "" {
						t.Skip(tc.notNested)
					}
					if strategy.s == FuseFS {
						skipUnlessFuseWorks(t)
					}

					// Create an executor and the action.
					cas, err := blobstore.New(t.Context(), filepath.Join(t.TempDir(), "cas"))
					if err != nil {
						t.Fatalf("blobstore.New: %v", err)
					}
					executor, err := New(filepath.Join(t.TempDir(), "exec"), cas, strategy.s, true, false)
					if err != nil {
						t.Fatalf("New: %v", err)
					}
					t.Cleanup(func() {
						if err := executor.Close(); err != nil {
							t.Errorf("Close: %v", err)
						}
					})
					action := putSymlinkTestAction(t, cas, &repb.Command{
						Arguments:         []string{"/bin/sh", "-c", tc.script},
						OutputFiles:       tc.files, //nolint:staticcheck
						OutputDirectories: tc.dirs,  //nolint:staticcheck
						OutputPaths:       tc.paths,
					})

					// Run the action.
					ar, err := executor.Execute(action)
					if err != nil {
						t.Fatalf("Execute: %v", err)
					}
					if ar.ExitCode != 0 {
						t.Fatalf("action exited with code %d, stderr: %s", ar.ExitCode, readTestBlob(t, cas, ar.StderrDigest))
					}

					// Check the reported symlinks.
					got := &repb.ActionResult{
						OutputFileSymlinks:      ar.OutputFileSymlinks,      //nolint:staticcheck
						OutputDirectorySymlinks: ar.OutputDirectorySymlinks, //nolint:staticcheck
						OutputSymlinks:          ar.OutputSymlinks,
					}
					want := &repb.ActionResult{
						OutputFileSymlinks:      tc.wantFileLinks, //nolint:staticcheck
						OutputDirectorySymlinks: tc.wantDirLinks,  //nolint:staticcheck
						OutputSymlinks:          tc.wantLinks,
					}
					if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
						t.Errorf("symlink outputs mismatch (-want +got):\n%s", diff)
					}
				})
			}
		})
	}
}

// skipUnlessNsjailWorks skips the test unless nsjail can run a command in
// fresh namespaces on this machine.
func skipUnlessNsjailWorks(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the sandbox strategies need nsjail, which only runs on Linux")
	}
	nsjailPath, err := exec.LookPath("nsjail")
	if err != nil {
		t.Skip("nsjail not found in PATH")
	}
	if output, err := exec.Command(nsjailPath, "--quiet", "--chroot", "/", "--cwd", "/", "--disable_rlimits", "--", "/bin/true").CombinedOutput(); err != nil {
		t.Skipf("nsjail can't run sandboxed commands: %v: %s", err, bytes.TrimSpace(output))
	}
}

// skipUnlessFuseWorks skips the test unless this process can mount FUSE
// filesystems.
func skipUnlessFuseWorks(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("fusermount3"); err != nil {
		t.Skip("fusermount3 not found in PATH")
	}
	f, err := os.OpenFile("/dev/fuse", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("FUSE device is not writable: %v", err)
	}
	_ = f.Close()
}

// putSymlinkTestAction stores an action that runs cmd on the input root shown
// in TestExecuteReportsSymlinkOutputsAsRequested, and loads it.
func putSymlinkTestAction(t *testing.T, cas *blobstore.ContentAddressableStorage, cmd *repb.Command) *model.Action {
	t.Helper()

	// Store the input files and directories.
	putFile := func(name, content string) *repb.FileNode {
		return &repb.FileNode{Name: name, Digest: putTestBlob(t, cas, []byte(content)).Proto()}
	}
	putDir := func(name string, dir *repb.Directory) *repb.DirectoryNode {
		return &repb.DirectoryNode{Name: name, Digest: putTestMessage(t, cas, dir)}
	}
	root := &repb.Directory{
		Files: []*repb.FileNode{putFile("in.txt", "input")},
		Directories: []*repb.DirectoryNode{
			putDir("a", &repb.Directory{Files: []*repb.FileNode{putFile("a.txt", "a")}}),
			putDir("b", &repb.Directory{Files: []*repb.FileNode{putFile("b.txt", "b")}}),
			putDir("indir", &repb.Directory{Files: []*repb.FileNode{putFile("f.txt", "f")}}),
		},
		Symlinks: []*repb.SymlinkNode{{Name: "inlink", Target: "in.txt"}},
	}

	// Store the command and the action, and load the action.
	actionDigest, err := digest.SHA256.FromProto(putTestMessage(t, cas, &repb.Action{
		CommandDigest:   putTestMessage(t, cas, cmd),
		InputRootDigest: putTestMessage(t, cas, root),
	}))
	if err != nil {
		t.Fatalf("FromProto: %v", err)
	}
	action, err := model.LoadAction(digest.SHA256, actionDigest, cas)
	if err != nil {
		t.Fatalf("model.LoadAction: %v", err)
	}
	return action
}

// putTestBlob stores data in cas under SHA-256 and returns its digest.
func putTestBlob(t *testing.T, cas *blobstore.ContentAddressableStorage, data []byte) digest.Digest {
	t.Helper()
	d, err := cas.Put(digest.SHA256, data)
	if err != nil {
		t.Fatalf("cas.Put: %v", err)
	}
	return d
}

// putTestMessage stores m in cas under SHA-256 and returns its digest.
func putTestMessage(t *testing.T, cas *blobstore.ContentAddressableStorage, m proto.Message) *repb.Digest {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	return putTestBlob(t, cas, b).Proto()
}

// readTestBlob returns the blob with digest d from cas, or a description of
// why it can't be read.
func readTestBlob(t *testing.T, cas *blobstore.ContentAddressableStorage, d *repb.Digest) string {
	t.Helper()
	dd, err := digest.SHA256.FromProto(d)
	if err != nil {
		return err.Error()
	}
	b, err := cas.Get(digest.SHA256, dd)
	if err != nil {
		return err.Error()
	}
	return string(b)
}
