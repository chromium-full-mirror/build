// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package model

import (
	"errors"
	"runtime"
	"testing"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/digest"
)

// putProto marshals m, stores it in cas, and returns its proto digest.
func putProto(t *testing.T, cas *blobstore.ContentAddressableStorage, m proto.Message) *repb.Digest {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	d, err := cas.Put(b)
	if err != nil {
		t.Fatalf("cas.Put: %v", err)
	}
	return d.ToProto()
}

// newCAS returns a fresh CAS rooted in t.TempDir().
func newCAS(t *testing.T) *blobstore.ContentAddressableStorage {
	t.Helper()
	cas, err := blobstore.New(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}
	return cas
}

// inputRootBuilder builds a small REAPI input tree in a CAS.
type inputRootBuilder struct {
	t   *testing.T
	cas *blobstore.ContentAddressableStorage
}

// dir uploads a Directory containing the given file names and subdirectory
// nodes, and returns its digest. files must be sorted and unique.
func (b *inputRootBuilder) dir(files []string, subDirs []*repb.DirectoryNode) *repb.Digest {
	b.t.Helper()
	d := &repb.Directory{}
	for _, name := range files {
		fd, err := b.cas.Put([]byte("contents of " + name))
		if err != nil {
			b.t.Fatalf("cas.Put file: %v", err)
		}
		d.Files = append(d.Files, &repb.FileNode{
			Name:   name,
			Digest: fd.ToProto(),
		})
	}
	d.Directories = subDirs
	return putProto(b.t, b.cas, d)
}

// uploadAction stores an Action with the given Command and input-root digests.
func uploadAction(t *testing.T, cas *blobstore.ContentAddressableStorage, cmd *repb.Command, inputRootDigest *repb.Digest) digest.Digest {
	t.Helper()
	cmdDigest := putProto(t, cas, cmd)
	a := &repb.Action{
		CommandDigest:   cmdDigest,
		InputRootDigest: inputRootDigest,
	}
	b, err := proto.Marshal(a)
	if err != nil {
		t.Fatalf("marshal action: %v", err)
	}
	d, err := cas.Put(b)
	if err != nil {
		t.Fatalf("cas.Put action: %v", err)
	}
	return d
}

// findOutputs returns the recorded outputs at the given trie key (e.g. "" for
// root, "src/" for a subdir).
func findOutputs(t *testing.T, ka *Action, key string) []KajiyaOutput {
	t.Helper()
	dir, ok := ka.InputTrie.Get([]byte(key))
	if !ok {
		t.Fatalf("trie key %q not present", key)
	}
	return dir.Outputs
}

// TestLoadAction_DefaultWorkingDir verifies that an empty WorkingDirectory
// (which defaults to ".") validates against the input root.
func TestLoadAction_DefaultWorkingDir(t *testing.T) {
	cas := newCAS(t)
	b := &inputRootBuilder{t: t, cas: cas}
	rootDigest := b.dir([]string{"main.go"}, nil)

	cmd := &repb.Command{
		Arguments: []string{"echo"},
		// WorkingDirectory left empty -> defaults to ".".
	}
	actionDigest := uploadAction(t, cas, cmd, rootDigest)

	ka, err := LoadAction(actionDigest, cas)
	if err != nil {
		t.Fatalf("LoadAction: %v", err)
	}
	if got, want := ka.WorkingDir, "."; got != want {
		t.Errorf("WorkingDir = %q, want %q", got, want)
	}
	if _, ok := ka.InputTrie.Get([]byte("")); !ok {
		t.Errorf("expected root entry under empty trie key")
	}
}

// TestLoadAction_RootLevelOutput verifies that an output file named at the
// root of the input tree is attached to the root KajiyaDirectory.
func TestLoadAction_RootLevelOutput(t *testing.T) {
	cas := newCAS(t)
	b := &inputRootBuilder{t: t, cas: cas}
	rootDigest := b.dir([]string{"main.go"}, nil)

	cmd := &repb.Command{
		Arguments:   []string{"compile"},
		OutputPaths: []string{"remote.out"},
	}
	actionDigest := uploadAction(t, cas, cmd, rootDigest)

	ka, err := LoadAction(actionDigest, cas)
	if err != nil {
		t.Fatalf("LoadAction: %v", err)
	}
	outs := findOutputs(t, ka, "")
	if len(outs) != 1 {
		t.Fatalf("root outputs = %v, want exactly one entry", outs)
	}
	if got, want := outs[0].Name, "remote.out"; got != want {
		t.Errorf("output name = %q, want %q", got, want)
	}
	if got, want := outs[0].Type, Unknown; got != want {
		t.Errorf("output type = %v, want %v", got, want)
	}
}

// TestLoadAction_NestedOutputUnderRoot verifies that an output path whose
// parent is a subdirectory of the root attaches to that subdirectory.
func TestLoadAction_NestedOutputUnderRoot(t *testing.T) {
	cas := newCAS(t)
	b := &inputRootBuilder{t: t, cas: cas}
	genDigest := b.dir(nil, nil) // empty "gen" subdir
	rootDigest := b.dir(nil, []*repb.DirectoryNode{
		{Name: "gen", Digest: genDigest},
	})

	cmd := &repb.Command{
		Arguments:   []string{"compile"},
		OutputPaths: []string{"gen/foo.o"},
	}
	actionDigest := uploadAction(t, cas, cmd, rootDigest)

	ka, err := LoadAction(actionDigest, cas)
	if err != nil {
		t.Fatalf("LoadAction: %v", err)
	}
	if outs := findOutputs(t, ka, ""); len(outs) != 0 {
		t.Errorf("root outputs = %v, want none", outs)
	}
	outs := findOutputs(t, ka, "gen/")
	if len(outs) != 1 {
		t.Fatalf("gen/ outputs = %v, want exactly one entry", outs)
	}
	if got, want := outs[0].Name, "foo.o"; got != want {
		t.Errorf("output name = %q, want %q", got, want)
	}
}

// TestLoadAction_OutputUnderWorkingDir verifies that outputs are joined with
// WorkingDir before parent lookup, so an output named "foo.o" under
// WorkingDirectory="src" attaches to the "src/" trie entry.
func TestLoadAction_OutputUnderWorkingDir(t *testing.T) {
	cas := newCAS(t)
	b := &inputRootBuilder{t: t, cas: cas}
	srcDigest := b.dir([]string{"main.go"}, nil)
	rootDigest := b.dir(nil, []*repb.DirectoryNode{
		{Name: "src", Digest: srcDigest},
	})

	cmd := &repb.Command{
		Arguments:        []string{"compile"},
		WorkingDirectory: "src",
		OutputPaths:      []string{"foo.o"},
	}
	actionDigest := uploadAction(t, cas, cmd, rootDigest)

	ka, err := LoadAction(actionDigest, cas)
	if err != nil {
		t.Fatalf("LoadAction: %v", err)
	}
	outs := findOutputs(t, ka, "src/")
	if len(outs) != 1 {
		t.Fatalf("src/ outputs = %v, want exactly one entry", outs)
	}
	if got, want := outs[0].Name, "foo.o"; got != want {
		t.Errorf("output name = %q, want %q", got, want)
	}
}

// TestLoadAction_OutputErrorPropagates verifies that errors from
// addOutputsToTrie surface from LoadAction as *InvalidActionError, so the
// gRPC layer can map them to codes.InvalidArgument rather than retriable
// codes.Internal. We use an output path that escapes the input root, which
// is rejected by the IsLocal check on every OS.
func TestLoadAction_OutputErrorPropagates(t *testing.T) {
	cas := newCAS(t)
	b := &inputRootBuilder{t: t, cas: cas}
	rootDigest := b.dir([]string{"main.go"}, nil)

	cmd := &repb.Command{
		Arguments:   []string{"compile"},
		OutputPaths: []string{"../escape.out"},
	}
	actionDigest := uploadAction(t, cas, cmd, rootDigest)

	_, err := LoadAction(actionDigest, cas)
	if err == nil {
		t.Fatal("LoadAction succeeded; want error for escaping output path")
	}
	var iaerr *InvalidActionError
	if !errors.As(err, &iaerr) {
		t.Errorf("LoadAction err = %T (%v); want *InvalidActionError", err, err)
	}
}

// TestLoadAction_WindowsReservedOutputName is a regression test for the
// case where an output path's basename is a Windows DOS reserved name. The
// production trigger was a chromium build action declaring "aux.out" on
// Windows Server bots, which filepath.IsLocal rejects. Pre-fix that
// rejection bubbled up as codes.Internal and triggered Siso's retry loop,
// causing a multi-minute hang on Windows CI.
//
// We use the bare basename "aux" because the with-extension form is
// rejected only on Windows versions whose RtlIsDosDeviceName_U still
// flags "aux.<ext>" (e.g. Windows Server 2019, Windows 10); Windows 11
// allows it. The bare basename is rejected on every Windows version.
// On non-Windows hosts filepath.IsLocal does not consider DOS names
// reserved, so the action loads cleanly; the test is skipped there.
func TestLoadAction_WindowsReservedOutputName(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("filepath.IsLocal only flags DOS reserved names on Windows")
	}
	cas := newCAS(t)
	b := &inputRootBuilder{t: t, cas: cas}
	rootDigest := b.dir([]string{"main.go"}, nil)

	cmd := &repb.Command{
		Arguments:   []string{"compile"},
		OutputPaths: []string{"aux"},
	}
	actionDigest := uploadAction(t, cas, cmd, rootDigest)

	_, err := LoadAction(actionDigest, cas)
	if err == nil {
		t.Fatal("LoadAction succeeded; want error for DOS-reserved output path on Windows")
	}
	var iaerr *InvalidActionError
	if !errors.As(err, &iaerr) {
		t.Errorf("LoadAction err = %T (%v); want *InvalidActionError", err, err)
	}
}
