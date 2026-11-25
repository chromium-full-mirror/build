// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/merkletree"
)

// setupToolchainSubtree writes a local and a remote toolchain to a new
// workspace and returns a Builder for it and the entries of the local
// toolchain's subtree.
func setupToolchainSubtree(t *testing.T) (*Builder, []merkletree.Entry) {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"toolchain/bin/clang":          "local clang",
		"toolchain/bin/clang++":        "local clang++",
		"toolchain/include/stddef.h":   "header",
		"remote-toolchain/bin/clang":   "remote clang",
		"remote-toolchain/bin/clang++": "remote clang++",
	} {
		fname := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fname, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	hfs, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hfs.Close(ctx) })
	ents, err := hfs.Entries(ctx, filepath.Join(dir, "toolchain"), []path.Path{"bin/clang", "bin/clang++", "include/stddef.h"})
	if err != nil {
		t.Fatal(err)
	}
	return &Builder{path: NewPath(dir, "out/siso"), hashFS: hfs}, ents
}

func TestSubstituteRemoteInputs(t *testing.T) {
	b, ents := setupToolchainSubtree(t)
	ents, err := b.substituteRemoteInputs(t.Context(), "toolchain", ents, map[path.Path]path.Path{
		"bin/clang":   "remote-toolchain/bin/clang++",
		"bin/clang++": "remote-toolchain/bin/clang++",
		"bin/tool":    "remote-toolchain/bin/tool",
	})
	if err != nil {
		t.Fatalf("substituteRemoteInputs(...)=_, %v; want nil err", err)
	}
	want := map[path.Path]string{
		"bin/clang":        "remote clang++",
		"bin/clang++":      "remote clang++",
		"include/stddef.h": "header",
	}
	got := make(map[path.Path]string, len(ents))
	for _, e := range ents {
		got[e.Name] = e.Data.Digest().String()
	}
	for name, content := range want {
		want[name] = blob.FromBytes(b.hashFS.DigestFunction(), string(name), []byte(content)).Digest().String()
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("substituteRemoteInputs(...) digests: diff -want +got:\n%s", diff)
	}
}

func TestSubstituteRemoteInputs_Errors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		remote  map[path.Path]path.Path
		wantErr error
	}{
		{
			name: "missing",
			remote: map[path.Path]path.Path{
				"bin/clang":   "remote-toolchain/bin/clang",
				"bin/clang++": "remote-toolchain/bin/missing",
			},
			wantErr: fs.ErrNotExist,
		},
		{
			name: "directory",
			remote: map[path.Path]path.Path{
				"bin/clang++": "remote-toolchain/bin/",
			},
			wantErr: errDirRemoteInput,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, ents := setupToolchainSubtree(t)
			_, err := b.substituteRemoteInputs(t.Context(), "toolchain", ents, tc.remote)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("substituteRemoteInputs(...)=_, %v; want %v", err, tc.wantErr)
			}
		})
	}
}
