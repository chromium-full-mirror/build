// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execute

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/merkletree"
)

func TestCanonicalizeDir(t *testing.T) {
	for _, tc := range []struct {
		name           string
		cmd            *Cmd
		ents           []merkletree.Entry
		treeInputs     []merkletree.TreeEntry
		wantEnts       []merkletree.Entry
		wantTreeInputs []merkletree.TreeEntry
	}{
		{
			name: "empty-dir",
			cmd: &Cmd{
				WorkDir: "",
			},
			ents: []merkletree.Entry{
				{
					Name: "out/Default",
				},
			},
			treeInputs: []merkletree.TreeEntry{
				{
					Name: "toolchain",
				},
			},
			wantEnts: []merkletree.Entry{
				{
					Name: "out/Default",
				},
			},
			wantTreeInputs: []merkletree.TreeEntry{
				{
					Name: "toolchain",
				},
			},
		},
		{
			name: "dot-dir",
			cmd: &Cmd{
				WorkDir: "",
			},
			ents: []merkletree.Entry{
				{
					Name: "out/Default",
				},
			},
			treeInputs: []merkletree.TreeEntry{
				{
					Name: "toolchain",
				},
			},
			wantEnts: []merkletree.Entry{
				{
					Name: "out/Default",
				},
			},
			wantTreeInputs: []merkletree.TreeEntry{
				{
					Name: "toolchain",
				},
			},
		},
		{
			name: "canonicalize-dir",
			cmd: &Cmd{
				WorkDir: "out/Default",
			},
			ents: []merkletree.Entry{
				{
					Name: "out/Default/file",
				},
				{
					Name: "out/Default/dir/file",
				},
				{
					Name: "file",
				},
				{
					Name: "dir/file",
				},
			},
			treeInputs: []merkletree.TreeEntry{
				{
					Name: "toolchain",
				},
				{
					Name: "out/Default/sdk",
				},
			},
			wantEnts: []merkletree.Entry{
				{
					Name: "out/x/file",
				},
				{
					Name: "out/x/dir/file",
				},
				{
					Name: "file",
				},
				{
					Name: "dir/file",
				},
			},
			wantTreeInputs: []merkletree.TreeEntry{
				{
					Name: "toolchain",
				},
				{
					Name: "out/x/sdk",
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			ents, treeInputs := tc.cmd.canonicalizeDir(ctx, tc.ents, tc.treeInputs)
			if diff := cmp.Diff(ents, tc.wantEnts, cmp.Comparer(cmpDigestData)); diff != "" {
				t.Errorf("ents: -got +want:\n%s", diff)
			}
			if diff := cmp.Diff(treeInputs, tc.wantTreeInputs); diff != "" {
				t.Errorf("treeInputs: -got +want:\n%s", diff)
			}
		})
	}
}

func cmpDigestData(a, b blob.Data) bool {
	return a.Digest() == b.Digest()
}

func TestEntriesFromResult_Auxiliary(t *testing.T) {
	ctx := t.Context()
	now := time.Now()

	d1 := digest.Digest{Hash: "hash1", SizeBytes: 1}
	d2 := digest.Digest{Hash: "hash2", SizeBytes: 2}
	tree := &rpb.Tree{
		Root: &rpb.Directory{
			Files: []*rpb.FileNode{
				{
					Name:   "file",
					Digest: d1.Proto(),
				},
			},
		},
	}
	treeData, err := blob.FromProtoMessage(digest.SHA256, tree)
	if err != nil {
		t.Fatal(err)
	}
	d3 := treeData.Digest()

	cmd := &Cmd{
		WorkDir: "out/Default",
		Outputs: []path.Path{
			"out/Default/main.o",
		},
		AuxiliaryLogOutputFiles: []path.Path{
			"out/Default/aux.d",
		},
		AuxiliaryLogOutputDirs: []path.Path{
			"out/Default/aux_dir",
		},
		outfiles: map[path.Path]bool{
			"out/Default/main.o": true,
		},
		actionResult: &rpb.ActionResult{
			OutputFiles: []*rpb.OutputFile{
				{Path: "main.o", Digest: d1.Proto()},
				{Path: "aux.d", Digest: d2.Proto()},
			},
			OutputDirectories: []*rpb.OutputDirectory{
				{Path: "aux_dir", TreeDigest: d3.Proto()},
			},
		},
	}

	ds := mockDataSource{}
	entries, additionalEntries := cmd.entriesFromResult(ctx, ds, now)

	// Verify main outputs (entries)
	wantEntries := []hashfs.UpdateEntry{
		{
			Name: "out/Default/main.o",
			Entry: &merkletree.Entry{
				Name: "out/Default/main.o",
				Data: blob.NewData(ds.Source(ctx, d1, "out/Default/main.o"), d1),
			},
			UpdatedTime: now,
			ModTime:     now,
			IsChanged:   true,
			Mode:        0644,
		},
	}
	if diff := cmp.Diff(entries, wantEntries, cmpopts.IgnoreUnexported(hashfs.UpdateEntry{}, merkletree.Entry{}, blob.Data{}), cmpopts.IgnoreFields(hashfs.UpdateEntry{}, "Action")); diff != "" {
		t.Errorf("entries mismatch (-got +want):\n%s", diff)
	}
	// cmp.Diff ignores blob.Data's unexported fields, so the content digest
	// is not compared above. Assert it explicitly: main.o must carry d1.
	if len(entries) == 1 {
		if got := entries[0].Entry.Data.Digest(); got != d1 {
			t.Errorf("main.o entry digest = %v, want %v", got, d1)
		}
	}

	// Verify additional outputs (should be empty for auxiliary outputs)
	if len(additionalEntries) != 0 {
		t.Errorf("additionalEntries: got %q, want empty", additionalEntries)
	}

}

// TestEntriesFromResult_DirOutput verifies entriesFromResult produces a CmdHash'd directory entry for the root plus CmdHash'd file entries for files under the directory output.
func TestEntriesFromResult_DirOutput(t *testing.T) {
	ctx := t.Context()
	now := time.Now()

	d1 := digest.Digest{Hash: "hash1", SizeBytes: 5}
	treeDg := digest.Digest{Hash: "tree1", SizeBytes: 10}

	cmd := &Cmd{
		WorkDir: "out/Default",
		OutputDirs: []path.Path{
			"out/Default/gendir",
		},
		outfiles: map[path.Path]bool{
			"out/Default/gendir": true,
		},
		CmdHash: []byte("test-cmd-hash"),
		actionResult: &rpb.ActionResult{
			OutputFiles: []*rpb.OutputFile{
				{Path: "gendir/hello.txt", Digest: d1.Proto()},
			},
			OutputDirectories: []*rpb.OutputDirectory{
				{Path: "gendir", TreeDigest: treeDg.Proto()},
			},
		},
	}

	ds := mockDataSource{}
	entries, additionalEntries := cmd.entriesFromResult(ctx, ds, now)

	// gendir/hello.txt is in entries with CmdHash because isOutputFile walks up
	// to "out/Default/gendir", which is in outfiles.
	wantEntries := []hashfs.UpdateEntry{
		{
			Name: "out/Default/gendir/hello.txt",
			Entry: &merkletree.Entry{
				Name: "out/Default/gendir/hello.txt",
				Data: blob.NewData(ds.Source(ctx, d1, "out/Default/gendir/hello.txt"), d1),
			},
			UpdatedTime: now,
			ModTime:     now,
			IsChanged:   true,
			Mode:        0644,
			CmdHash:     []byte("test-cmd-hash"),
		},
		{
			Name: "out/Default/gendir",
			Entry: &merkletree.Entry{
				Name: "out/Default/gendir",
			},
			UpdatedTime: now,
			ModTime:     now,
			IsChanged:   true,
			Mode:        fs.FileMode(0755) | fs.ModeDir,
			CmdHash:     []byte("test-cmd-hash"),
		},
	}
	if diff := cmp.Diff(entries, wantEntries, cmpopts.IgnoreUnexported(hashfs.UpdateEntry{}, merkletree.Entry{}, blob.Data{}), cmpopts.IgnoreFields(hashfs.UpdateEntry{}, "Action")); diff != "" {
		t.Errorf("entries mismatch (-got +want):\n%s", diff)
	}
	// cmp.Diff ignores blob.Data's unexported fields, so assert the flattened
	// file's content digest explicitly: gendir/hello.txt must carry d1.
	gotHello := false
	for _, e := range entries {
		if e.Name != "out/Default/gendir/hello.txt" {
			continue
		}
		gotHello = true
		if got := e.Entry.Data.Digest(); got != d1 {
			t.Errorf("gendir/hello.txt digest = %v, want %v", got, d1)
		}
	}
	if !gotHello {
		t.Errorf("entries missing out/Default/gendir/hello.txt; got %v", entries)
	}
	if len(additionalEntries) != 0 {
		t.Errorf("additionalEntries: got %d, want 0", len(additionalEntries))
	}
}

// TestEntriesFromResult_DirOutput_SubdirsHaveCmdHash verifies every dir entry of a directory output carries CmdHash, not just the root.
// CmdHash marks an entry as generated output and drives state-reload reconciliation; subdirs are as much a product of the step as the root.
func TestEntriesFromResult_DirOutput_SubdirsHaveCmdHash(t *testing.T) {
	ctx := t.Context()
	now := time.Now()

	d1 := digest.Digest{Hash: "hash1", SizeBytes: 5}
	rootTreeDg := digest.Digest{Hash: "tree1", SizeBytes: 10}
	subTreeDg := digest.Digest{Hash: "tree2", SizeBytes: 8}
	deepTreeDg := digest.Digest{Hash: "tree3", SizeBytes: 6}

	cmd := &Cmd{
		WorkDir: "out/Default",
		OutputDirs: []path.Path{
			"out/Default/gendir",
		},
		outfiles: map[path.Path]bool{
			"out/Default/gendir": true,
		},
		CmdHash: []byte("test-cmd-hash"),
		actionResult: &rpb.ActionResult{
			OutputFiles: []*rpb.OutputFile{
				{Path: "gendir/hello.txt", Digest: d1.Proto()},
				{Path: "gendir/sub/nested.txt", Digest: d1.Proto()},
			},
			// The root dir and every subdir from merkletree.Traverse.
			OutputDirectories: []*rpb.OutputDirectory{
				{Path: "gendir", TreeDigest: rootTreeDg.Proto()},
				{Path: "gendir/sub", TreeDigest: subTreeDg.Proto()},
				{Path: "gendir/sub/deep", TreeDigest: deepTreeDg.Proto()},
			},
		},
	}

	ds := mockDataSource{}
	entries, _ := cmd.entriesFromResult(ctx, ds, now)

	// Every dir entry (root and subdirs) must be present with CmdHash.
	want := map[string]bool{
		"out/Default/gendir":          false,
		"out/Default/gendir/sub":      false,
		"out/Default/gendir/sub/deep": false,
	}
	for i := range entries {
		if _, ok := want[string(entries[i].Name)]; !ok {
			continue
		}
		want[string(entries[i].Name)] = true
		if len(entries[i].CmdHash) == 0 {
			t.Errorf("%s CmdHash is empty; want non-empty", entries[i].Name)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing dir entry %s", name)
		}
	}
}

// TestEntriesFromResult_DirOutput_SymlinkInside verifies a symlink inside a directory output (flattened into result.OutputSymlinks) is recorded as a CmdHash'd entry.
func TestEntriesFromResult_DirOutput_SymlinkInside(t *testing.T) {
	ctx := t.Context()
	now := time.Now()

	treeDg := digest.Digest{Hash: "tree1", SizeBytes: 10}

	cmd := &Cmd{
		WorkDir: "out/Default",
		OutputDirs: []path.Path{
			"out/Default/gendir",
		},
		outfiles: map[path.Path]bool{
			"out/Default/gendir": true,
		},
		CmdHash: []byte("test-cmd-hash"),
		actionResult: &rpb.ActionResult{
			OutputSymlinks: []*rpb.OutputSymlink{
				{Path: "gendir/link", Target: "hello.txt"},
			},
			OutputDirectories: []*rpb.OutputDirectory{
				{Path: "gendir", TreeDigest: treeDg.Proto()},
			},
		},
	}

	ds := mockDataSource{}
	entries, additionalEntries := cmd.entriesFromResult(ctx, ds, now)

	var linkEnt *hashfs.UpdateEntry
	for i := range entries {
		if entries[i].Name == "out/Default/gendir/link" {
			linkEnt = &entries[i]
			break
		}
	}
	if linkEnt == nil {
		t.Fatalf("missing symlink entry out/Default/gendir/link; got entries: %v", entries)
	}
	if len(linkEnt.CmdHash) == 0 {
		t.Errorf("symlink CmdHash is empty; want non-empty")
	}
	if linkEnt.Mode&fs.ModeSymlink == 0 {
		t.Errorf("symlink mode = %v; want ModeSymlink bit set", linkEnt.Mode)
	}
	if linkEnt.Entry.Target != "hello.txt" {
		t.Errorf("symlink target = %q; want %q", linkEnt.Entry.Target, "hello.txt")
	}
	if len(additionalEntries) != 0 {
		t.Errorf("additionalEntries: got %d, want 0", len(additionalEntries))
	}
}

func TestIsOutputFile(t *testing.T) {
	cmd := &Cmd{
		outfiles: map[path.Path]bool{
			"out/Default/main.o": true,
			"out/Default/gendir": true,
		},
	}
	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{name: "exact match", path: "out/Default/main.o", want: true},
		{name: "dir match", path: "out/Default/gendir", want: true},
		{name: "file under dir", path: "out/Default/gendir/foo.txt", want: true},
		{name: "nested under dir", path: "out/Default/gendir/sub/bar.txt", want: true},
		{name: "deeply nested under dir", path: "out/Default/gendir/a/b/c/d/e.txt", want: true},
		{name: "unrelated file", path: "out/Default/other.o", want: false},
		{name: "partial prefix", path: "out/Default/gendir_extra/foo.txt", want: false},
		{name: "absolute path no match", path: "/usr/local/bin/tool", want: false},
		{name: "root path", path: "/", want: false},
		{name: "empty path", path: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := cmd.isOutputFile(path.Path(tc.path))
			if got != tc.want {
				t.Errorf("isOutputFile(%q) = %v; want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestIsOutputFile_ForwardSlashSemantics verifies isOutputFile uses path.Dir (always forward slash), not filepath.Dir.
// Regression guard: c.outfiles keys are forward-slash, so a filepath.Dir walk would fail to match parents on Windows (Linux can't prove this alone, since the two are identical there).
func TestIsOutputFile_ForwardSlashSemantics(t *testing.T) {
	cmd := &Cmd{
		outfiles: map[path.Path]bool{
			"out/Default/gendir": true,
		},
	}
	if !cmd.isOutputFile("out/Default/gendir/a/b/c.txt") {
		t.Error("isOutputFile should match a nested file under an output dir regardless of platform")
	}
}

type mockDataSource struct{}

func (mockDataSource) Source(ctx context.Context, d digest.Digest, name string) blob.Source {
	return nil
}

func (mockDataSource) Close(ctx context.Context) error { return nil }

// TestRecordPreOutputs verifies the pre-execution output snapshot for a racing
// remote racer (SkipRecordOutputs set, sharing hashFS with the local racer):
//
//   - Outputs that already exist on disk must be snapshotted, so that when the
//     remote side wins, runRacing's RecordOutputs can compare pre/post content
//     and honor restat_content. (My earlier fix skipped the snapshot entirely
//     and regressed this.)
//   - Outputs that do not exist yet must NOT be recorded as negative hashFS
//     entries: doing so races the local racer producing the same file and
//     poisons the cmdhash-less depfile, surfacing as a spurious
//     "failed to get depfile". (The original code regressed this.)
//
// This test fails for both wrong implementations and passes only for the
// existing-only snapshot.
func TestRecordPreOutputs(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatalf("hashfs.New: %v", err)
	}
	defer hashFS.Close(ctx)

	// Three outputs the racing remote racer snapshots before execution:
	//   onDisk     - already materialized on local disk (a rebuild)
	//   hashfsOnly - left only in hashFS/CAS by a prior remote result, not on disk
	//   depfile    - absent from both disk and hashFS (a fresh action)
	onDisk := "out/ondisk.o"
	hashfsOnly := "out/remote.o"
	depfile := "out/foo.o.d"
	if err := os.WriteFile(filepath.Join(dir, onDisk), []byte("old object"), 0o644); err != nil {
		t.Fatal(err)
	}
	rd := digest.Digest{Hash: "remotehash", SizeBytes: 7}
	if err := hashFS.Update(ctx, dir, []hashfs.UpdateEntry{{
		Name:    path.Path(hashfsOnly),
		Entry:   &merkletree.Entry{Name: path.Path(hashfsOnly), Data: blob.NewData(nil, rd)},
		Mode:    0o644,
		CmdHash: []byte("cmdhash"),
	}}); err != nil {
		t.Fatalf("Update(%q): %v", hashfsOnly, err)
	}

	cmd := &Cmd{
		WorkspaceRoot:     dir,
		Outputs:           []path.Path{path.Path(onDisk), path.Path(hashfsOnly)},
		Depfile:           path.Path(depfile),
		HashFS:            hashFS,
		SkipRecordOutputs: true, // the racing remote racer
	}

	cmd.RecordPreOutputs(ctx)

	// (1) restat: both the on-disk output and the hashFS-only (cached, not
	// materialized locally) output must be in the snapshot, so a later
	// RecordOutputs can compare pre/post content. Dropping either dirties
	// downstream steps under restat_content.
	got := map[string]bool{}
	for _, e := range cmd.preOutputEntries {
		got[string(e.Name)] = true
	}
	for _, want := range []string{onDisk, hashfsOnly} {
		if !got[want] {
			t.Errorf("preOutputEntries missing %q; restat_content snapshot lost (have %v)", want, got)
		}
	}

	// (2) no poison: the depfile is absent from both disk and hashFS, so it must
	// not be recorded as a negative hashFS entry. After the command writes it,
	// reading it back must succeed.
	if err := os.WriteFile(filepath.Join(dir, depfile), []byte("foo.o: foo.c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := hashFS.ReadFile(ctx, dir, path.Path(depfile)); err != nil {
		t.Errorf("ReadFile(%q) after RecordPreOutputs = %v; want success (absent output must not be poisoned)", depfile, err)
	}
}

// TestEntriesFromResult_DirOutput_AuxiliaryInnerFile verifies a file inside a directory output is still recorded when its path is also an auxiliary log output.
// The auxiliary skip (IsAuxiliary && !outfiles[fname]) must not swallow inner files, which are never in c.outfiles (only the dir root is).
func TestEntriesFromResult_DirOutput_AuxiliaryInnerFile(t *testing.T) {
	ctx := t.Context()
	now := time.Now()

	d1 := digest.Digest{Hash: "hash1", SizeBytes: 5}
	cmd := &Cmd{
		WorkDir:                 "out/Default",
		OutputDirs:              []path.Path{"out/Default/gendir"},
		AuxiliaryLogOutputFiles: []path.Path{"out/Default/gendir/info.json"},
		outfiles: map[path.Path]bool{
			"out/Default/gendir": true,
		},
		CmdHash: []byte("test-cmd-hash"),
		actionResult: &rpb.ActionResult{
			OutputFiles: []*rpb.OutputFile{
				{Path: "gendir/info.json", Digest: d1.Proto()},
			},
		},
	}

	ds := mockDataSource{}
	entries, _ := cmd.entriesFromResult(ctx, ds, now)
	found := false
	for _, e := range entries {
		if e.Name == "out/Default/gendir/info.json" {
			found = true
		}
	}
	if !found {
		t.Errorf("dir-output inner file out/Default/gendir/info.json not recorded (dropped by auxiliary skip)")
	}
}

// TestRecordOutputsFromLocal_JailDepfile exercises the nsjail capture path with a depfile: each output (including the depfile) must be moved out of the jail exactly once.
// AllOutputs already includes the depfile, so a duplicate append would os.Rename it twice and the second rename fails ENOENT.
func TestRecordOutputsFromLocal_JailDepfile(t *testing.T) {
	ctx := t.Context()

	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	jail := filepath.Join(root, "jail")
	if err := os.MkdirAll(jail, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jail, "foo.o"), []byte("obj"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jail, "foo.o.d"), []byte("foo.o: foo.c\n"), 0644); err != nil {
		t.Fatal(err)
	}

	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hashFS.Close(ctx)
	if err := hashFS.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}

	c := &Cmd{
		WorkspaceRoot:     root,
		ExecRootInJailDir: jail,
		Outputs:           []path.Path{"foo.o"},
		Depfile:           "foo.o.d",
		HashFS:            hashFS,
	}
	c.InitOutputs()

	if err := c.RecordOutputsFromLocal(ctx, time.Now()); err != nil {
		t.Fatalf("RecordOutputsFromLocal: %v (depfile renamed twice out of jail)", err)
	}
	for _, out := range []string{"foo.o", "foo.o.d"} {
		if _, err := os.Stat(filepath.Join(root, out)); err != nil {
			t.Errorf("output %q not captured from jail: %v", out, err)
		}
	}
}

// TestRecordOutputsFromLocal_JailDepfileAlsoOutput covers a depfile path that is also a declared output: AllOutputs lists it twice but the jail capture must rename it only once.
func TestRecordOutputsFromLocal_JailDepfileAlsoOutput(t *testing.T) {
	ctx := t.Context()

	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	jail := filepath.Join(root, "jail")
	if err := os.MkdirAll(jail, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jail, "foo.o"), []byte("obj"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jail, "foo.o.d"), []byte("foo.o: foo.c\n"), 0644); err != nil {
		t.Fatal(err)
	}

	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hashFS.Close(ctx)
	if err := hashFS.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}

	// foo.o.d is both a declared output and the depfile.
	c := &Cmd{
		WorkspaceRoot:     root,
		ExecRootInJailDir: jail,
		Outputs:           []path.Path{"foo.o", "foo.o.d"},
		Depfile:           "foo.o.d",
		HashFS:            hashFS,
	}
	c.InitOutputs()

	if err := c.RecordOutputsFromLocal(ctx, time.Now()); err != nil {
		t.Fatalf("RecordOutputsFromLocal: %v (depfile-as-output renamed twice out of jail)", err)
	}
	for _, out := range []string{"foo.o", "foo.o.d"} {
		if _, err := os.Stat(filepath.Join(root, out)); err != nil {
			t.Errorf("output %q not captured from jail: %v", out, err)
		}
	}
}

// TestComputeOutputEntries_DirOnlyEdgeHash is a regression test: for a dir-only
// output (no file Outputs), the edge hash must land on the declared directory
// entry, not on entries[0] (a flattened child file). outputMtime reads the edge
// hash from the declared output, and needToRun silently skips edge-change
// detection when it is empty, so a misplaced edge hash misses rebuilds.
func TestComputeOutputEntries_DirOnlyEdgeHash(t *testing.T) {
	c := &Cmd{
		WorkDir:    "out/Default",
		OutputDirs: []path.Path{"out/Default/gen"},
		EdgeHash:   []byte("edge-hash"),
		CmdHash:    []byte("cmd-hash"),
	}
	// entriesFromResult emits flattened child files before the directory node.
	entries := []hashfs.UpdateEntry{
		{Name: "out/Default/gen/inner.txt", Entry: &merkletree.Entry{Name: "out/Default/gen/inner.txt"}, Mode: 0644, IsChanged: true},
		{Name: "out/Default/gen", Entry: &merkletree.Entry{Name: "out/Default/gen"}, Mode: fs.ModeDir | 0755, IsChanged: true},
	}
	got := c.computeOutputEntries(entries, time.Unix(2000, 0), c.CmdHash)
	byName := make(map[string]hashfs.UpdateEntry, len(got))
	for _, e := range got {
		byName[string(e.Name)] = e
	}
	if string(byName["out/Default/gen"].EdgeHash) != string(c.EdgeHash) {
		t.Errorf("dir output entry EdgeHash=%q; want %q (edge hash must be on the declared dir output; otherwise outputMtime reads empty and edge changes are missed)", byName["out/Default/gen"].EdgeHash, c.EdgeHash)
	}
}

// singleFileDirTree registers a REAPI tree blob (and its content blob) for a
// directory containing exactly one file, and returns the tree digest.
func singleFileDirTree(t *testing.T, ds *blob.Store, member, content string) digest.Digest {
	t.Helper()
	cd := blob.FromBytes(digest.SHA256, member, []byte(content))
	ds.Set(cd)
	tree := &rpb.Tree{Root: &rpb.Directory{Files: []*rpb.FileNode{{Name: member, Digest: cd.Digest().Proto()}}}}
	td, err := blob.FromProtoMessage(digest.SHA256, tree)
	if err != nil {
		t.Fatal(err)
	}
	ds.Set(td)
	return td.Digest()
}

// TestRecordOutputs_DirTreeReplacesStaleMembers is a regression test: when a
// remote/cache directory output is re-recorded with a different tree, members
// from the previous tree must not linger. Recording flattens and upserts the
// new tree but, without an explicit prune, HashFS.Update merges it into the old
// one (the local path avoids this by reading disk truth), so a consumer could
// still see the removed member.
func TestRecordOutputs_DirTreeReplacesStaleMembers(t *testing.T) {
	ctx := t.Context()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hashFS.Close(ctx) })
	if err := hashFS.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}

	ds := blob.NewStore()
	td1 := singleFileDirTree(t, ds, "old.txt", "OLD")
	td2 := singleFileDirTree(t, ds, "new.txt", "NEW")

	c := &Cmd{
		WorkspaceRoot: root,
		HashFS:        hashFS,
		OutputDirs:    []path.Path{"gen"},
		CmdHash:       []byte("cmd"),
		EdgeHash:      []byte("edge"),
	}
	c.InitOutputs()
	result := func(td digest.Digest) *rpb.ActionResult {
		return &rpb.ActionResult{OutputDirectories: []*rpb.OutputDirectory{{Path: "gen", TreeDigest: td.Proto()}}}
	}
	dsrc := storeDataSource{s: ds}

	c.SetActionResult(result(td1), false)
	if err := c.RecordOutputs(ctx, dsrc, time.Now()); err != nil {
		t.Fatalf("RecordOutputs #1: %v", err)
	}
	if _, err := hashFS.Stat(ctx, root, "gen/old.txt"); err != nil {
		t.Fatalf("after #1: gen/old.txt should be recorded: %v", err)
	}

	// Re-record the same dir output (same cmd/edge/action) with a different tree.
	c.SetActionResult(result(td2), false)
	if err := c.RecordOutputs(ctx, dsrc, time.Now()); err != nil {
		t.Fatalf("RecordOutputs #2: %v", err)
	}
	if _, err := hashFS.Stat(ctx, root, "gen/new.txt"); err != nil {
		t.Errorf("after #2: gen/new.txt missing: %v", err)
	}
	if _, err := hashFS.Stat(ctx, root, "gen/old.txt"); err == nil {
		t.Errorf("after #2: gen/old.txt still present; the new tree must replace the old, not merge")
	}
}

// TestInputTree_RejectsDirectoryRemoteInput verifies a directory (trailing-slash) RemoteInputs value is rejected.
// HashFS.Entries would expand it into files, so the reverse map keyed on the directory path no longer matches and the remap is silently dropped.
func TestInputTree_RejectsDirectoryRemoteInput(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hashFS.Close(ctx)
	if err := hashFS.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}

	c := &Cmd{
		WorkspaceRoot: root,
		WorkDir:       "out/siso",
		HashFS:        hashFS,
		RemoteInputs: map[path.Path]path.Path{
			"remote/extracted/": "obj/extracted/",
		},
	}
	_, err = c.inputTree(ctx)
	if err == nil {
		t.Fatal("inputTree with a directory remote_inputs value = nil error; want rejection")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("inputTree error = %q; want it to mention directory", err)
	}
}

// TestDeclaredOutputsAndAllOutputs verifies DeclaredOutputs returns file and directory outputs without the depfile, and AllOutputs adds the depfile.
func TestDeclaredOutputsAndAllOutputs(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cmd          Cmd
		wantDeclared []string
		wantAll      []string
	}{
		{
			name:         "files only",
			cmd:          Cmd{Outputs: []path.Path{"a.o", "b.o"}},
			wantDeclared: []string{"a.o", "b.o"},
			wantAll:      []string{"a.o", "b.o"},
		},
		{
			name:         "files and dirs",
			cmd:          Cmd{Outputs: []path.Path{"a.o"}, OutputDirs: []path.Path{"gen"}},
			wantDeclared: []string{"a.o", "gen"},
			wantAll:      []string{"a.o", "gen"},
		},
		{
			name:         "files and depfile",
			cmd:          Cmd{Outputs: []path.Path{"a.o"}, Depfile: "a.o.d"},
			wantDeclared: []string{"a.o"},
			wantAll:      []string{"a.o", "a.o.d"},
		},
		{
			name:         "files dirs and depfile",
			cmd:          Cmd{Outputs: []path.Path{"a.o"}, OutputDirs: []path.Path{"gen"}, Depfile: "a.o.d"},
			wantDeclared: []string{"a.o", "gen"},
			wantAll:      []string{"a.o", "gen", "a.o.d"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.wantDeclared, path.Strings(tc.cmd.DeclaredOutputs())); diff != "" {
				t.Errorf("DeclaredOutputs (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantAll, path.Strings(tc.cmd.AllOutputs())); diff != "" {
				t.Errorf("AllOutputs (-want +got):\n%s", diff)
			}
		})
	}
}

// TestAllOutputsNoAlias verifies AllOutputs does not write the depfile into Outputs' backing array (which can have spare capacity from stepFileOutputs).
func TestAllOutputsNoAlias(t *testing.T) {
	outputs := make([]path.Path, 1, 4) // len 1, cap 4: spare capacity
	outputs[0] = "a.o"
	c := Cmd{Outputs: outputs, Depfile: "a.o.d"}
	_ = c.AllOutputs()
	// The depfile must not have leaked into Outputs' backing array.
	full := c.Outputs[:cap(c.Outputs)]
	for _, s := range full[len(c.Outputs):] {
		if s == "a.o.d" {
			t.Errorf("AllOutputs wrote depfile into Outputs' backing array: %v", full)
		}
	}
}

// TestRenameFromJail verifies renameFromJail captures an output out of the nsjail exec root, replacing a non-empty pre-created destination cleanly (a plain os.Rename of a dir onto a non-empty dir fails ENOTEMPTY).
func TestRenameFromJail(t *testing.T) {
	t.Run("file overwrites destination", func(t *testing.T) {
		dir := t.TempDir()
		jail := filepath.Join(dir, "jail")
		dst := filepath.Join(dir, "dst")
		if err := os.MkdirAll(jail, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jail, "out.txt"), []byte("NEW"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dst, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, "out.txt"), []byte("OLD"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := renameFromJail(filepath.Join(jail, "out.txt"), filepath.Join(dst, "out.txt")); err != nil {
			t.Fatalf("renameFromJail(file): %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dst, "out.txt"))
		if err != nil || string(got) != "NEW" {
			t.Fatalf("dst content = %q, %v; want NEW", got, err)
		}
	})

	t.Run("dir onto populated destination", func(t *testing.T) {
		dir := t.TempDir()
		jail := filepath.Join(dir, "jail", "gen")
		dst := filepath.Join(dir, "out", "gen")
		if err := os.MkdirAll(jail, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jail, "new.txt"), []byte("NEW"), 0644); err != nil {
			t.Fatal(err)
		}
		// Destination pre-created and populated by a prior build.
		if err := os.MkdirAll(dst, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, "stale.txt"), []byte("OLD"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := renameFromJail(jail, dst); err != nil {
			t.Fatalf("renameFromJail(dir onto populated dir): %v", err)
		}
		if _, err := os.Stat(filepath.Join(dst, "new.txt")); err != nil {
			t.Errorf("captured dir missing new.txt: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dst, "stale.txt")); !os.IsNotExist(err) {
			t.Errorf("stale file from previous build survived capture: err=%v", err)
		}
	})
}

func TestOutermostPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{"file nested in dir output", []string{"gen/foo.h", "gen"}, []string{"gen"}},
		{"nested dir outputs", []string{"gen", "gen/sub"}, []string{"gen"}},
		{"siblings, no parent output", []string{"gen/a.o", "gen/b.o"}, []string{"gen/a.o", "gen/b.o"}},
		{"exact duplicate (depfile also output)", []string{"foo.o", "foo.o.d", "foo.o.d"}, []string{"foo.o", "foo.o.d"}},
		{"unrelated", []string{"gen", "other.o"}, []string{"gen", "other.o"}},
		// Sorted order can interleave a sibling between a directory root and
		// its contents ('.' < '/'): "gen/foo.stamp" sorts between "gen/foo"
		// and "gen/foo/a.o". The sibling must stay; the nested file must go.
		{"interleaved sibling", []string{"gen/foo", "gen/foo.stamp", "gen/foo/a.o"}, []string{"gen/foo", "gen/foo.stamp"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := OutermostPaths(path.Paths(tc.in))
			if diff := cmp.Diff(tc.want, path.Strings(got), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("OutermostPaths(%v) mismatch (-want +got):\n%s", tc.in, diff)
			}
		})
	}
}

// TestRecordOutputsFromLocal_JailNestedDirOutput covers a directory output with a file output nested inside it: the jail capture must rename only the outermost output (the directory).
// Renaming the nested file separately and then the directory would let the directory's RemoveAll of its pre-created destination clobber the just-moved nested file.
func TestRecordOutputsFromLocal_JailNestedDirOutput(t *testing.T) {
	ctx := t.Context()

	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	jail := filepath.Join(root, "jail")
	// The action produced gen/ with a nested file under it.
	if err := os.MkdirAll(filepath.Join(jail, "gen"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jail, "gen", "foo.h"), []byte("H"), 0644); err != nil {
		t.Fatal(err)
	}
	// Mimic ensureActionOutputDirs pre-creating the dir output's destination.
	if err := os.MkdirAll(filepath.Join(root, "gen"), 0755); err != nil {
		t.Fatal(err)
	}

	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hashFS.Close(ctx)
	if err := hashFS.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}

	c := &Cmd{
		WorkspaceRoot:     root,
		ExecRootInJailDir: jail,
		Outputs:           []path.Path{"gen/foo.h"},
		OutputDirs:        []path.Path{"gen"},
		HashFS:            hashFS,
	}
	c.InitOutputs()

	if err := c.RecordOutputsFromLocal(ctx, time.Now()); err != nil {
		t.Fatalf("RecordOutputsFromLocal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "gen", "foo.h")); err != nil {
		t.Errorf("nested output gen/foo.h lost during jail capture: %v (the directory output's RemoveAll clobbered it)", err)
	}
}
