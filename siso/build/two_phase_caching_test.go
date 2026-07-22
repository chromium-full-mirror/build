// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/merkletree"
	"go.chromium.org/build/siso/reapi/reapitest"
)

func TestMatchInputRoot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("two phase caching test is only on linux")
	}
	for _, tc := range []struct {
		name       string
		setup      func(*testing.T, string) []merkletree.Entry
		wantErr    bool
		wantInputs []string
	}{
		{
			// Symlink pointing to a directory when directory is expected (b/527756492 fix).
			name: "symlink_to_dir",
			setup: func(t *testing.T, dir string) []merkletree.Entry {
				t.Helper()
				// Local filesystem setup:
				// prebuilts/clang-tools/linux-x86/lib/clang/22/include/stddef.h
				// prebuilts/clang-tools/linux-x86/clang-headers -> lib/clang/22/include
				incDir := filepath.Join(dir, "prebuilts/clang-tools/linux-x86/lib/clang/22/include")
				err := os.MkdirAll(incDir, 0755)
				if err != nil {
					t.Fatal(err)
				}
				headerContent := []byte("// stddef.h content")
				err = os.WriteFile(filepath.Join(incDir, "stddef.h"), headerContent, 0644)
				if err != nil {
					t.Fatal(err)
				}

				symlinkPath := filepath.Join(dir, "prebuilts/clang-tools/linux-x86/clang-headers")
				err = os.Symlink("lib/clang/22/include", symlinkPath)
				if err != nil {
					t.Fatal(err)
				}
				return []merkletree.Entry{
					{
						Name: path.Path("prebuilts/clang-tools/linux-x86/clang-headers/stddef.h"),
						Data: blob.FromBytes(digest.SHA256, "stddef.h", headerContent),
					},
				}
			},
			wantInputs: []string{
				"prebuilts/clang-tools/linux-x86/clang-headers/stddef.h",
			},
		},
		{
			// Symlink pointing to a file when directory is expected.
			name: "symlink_to_file_when_dir_wanted",
			setup: func(t *testing.T, dir string) []merkletree.Entry {
				t.Helper()
				// Local filesystem setup:
				// prebuilts/clang-tools/linux-x86/somefile.txt
				// prebuilts/clang-tools/linux-x86/clang-headers -> somefile.txt
				baseDir := filepath.Join(dir, "prebuilts/clang-tools/linux-x86")
				err := os.MkdirAll(baseDir, 0755)
				if err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(filepath.Join(baseDir, "somefile.txt"), []byte("not a dir"), 0644)
				if err != nil {
					t.Fatal(err)
				}
				err = os.Symlink("somefile.txt", filepath.Join(baseDir, "clang-headers"))
				if err != nil {
					t.Fatal(err)
				}
				return []merkletree.Entry{
					{
						Name: path.Path("prebuilts/clang-tools/linux-x86/clang-headers/stddef.h"),
						Data: blob.FromBytes(digest.SHA256, "stddef.h", []byte("content")),
					},
				}
			},
			wantErr: true,
		},
		{
			// Regular directory matching directory.
			name: "regular_dir",
			setup: func(t *testing.T, dir string) []merkletree.Entry {
				t.Helper()
				// Local filesystem setup:
				headersDir := filepath.Join(dir, "prebuilts/clang-tools/linux-x86/clang-headers")
				err := os.MkdirAll(headersDir, 0755)
				if err != nil {
					t.Fatal(err)
				}
				headerContent := []byte("// stddef.h content")
				err = os.WriteFile(filepath.Join(headersDir, "stddef.h"), headerContent, 0644)
				if err != nil {
					t.Fatal(err)
				}
				return []merkletree.Entry{
					{
						Name: path.Path("prebuilts/clang-tools/linux-x86/clang-headers/stddef.h"),
						Data: blob.FromBytes(digest.SHA256, "stddef.h", headerContent),
					},
				}
			},
			wantInputs: []string{
				"prebuilts/clang-tools/linux-x86/clang-headers/stddef.h",
			},
		},
		// File digest mismatch.
		{
			name: "file_digest_mismatch",
			setup: func(t *testing.T, dir string) []merkletree.Entry {
				t.Helper()
				filePath := filepath.Join(dir, "foo.txt")
				err := os.WriteFile(filePath, []byte("local content"), 0644)
				if err != nil {
					t.Fatal(err)
				}
				return []merkletree.Entry{
					{
						Name: path.Path("foo.txt"),
						Data: blob.FromBytes(digest.SHA256, "foo.txt", []byte("remote content")),
					},
				}
			},
			wantErr: true,
		},
		{
			// Symlink target mismatch.
			name: "symlink_target_mismatch",
			setup: func(t *testing.T, dir string) []merkletree.Entry {
				t.Helper()
				symPath := filepath.Join(dir, "link.txt")
				err := os.Symlink("targetA", symPath)
				if err != nil {
					t.Fatal(err)
				}
				return []merkletree.Entry{
					{
						Name:   path.Path("link.txt"),
						Target: "targetB",
					},
				}
			},
			wantErr: true,
		},
	} {
		ctx := t.Context()
		dir := t.TempDir()
		dir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}

		entries := tc.setup(t, dir)

		// Remote CAS setup:
		// prebuilts/clang-tools/linux-x86/clang-headers/stddef.h (clang-headers recorded as DirectoryNode)
		ds := blob.NewStore()
		tree := merkletree.New(digest.SHA256, ds)
		for _, ent := range entries {
			tree.Set(ent)
		}
		inputRootDigest, err := tree.Build(ctx)
		if err != nil {
			t.Fatal(err)
		}

		fakere := &reapitest.Fake{}
		reclient := reapitest.New(ctx, t, fakere)
		_, err = reclient.UploadAll(ctx, ds)
		if err != nil {
			t.Fatal(err)
		}

		hashFS, err := hashfs.New(ctx, hashfs.Option{})
		if err != nil {
			t.Fatal(err)
		}
		defer hashFS.Close(context.WithoutCancel(ctx))
		err = hashFS.WaitReady(ctx)
		if err != nil {
			t.Fatal(err)
		}

		b := &Builder{
			path:        NewPath(dir, "out/siso"),
			hashFS:      hashFS,
			reapiclient: reclient,
		}
		rt := reapiTwoPhaseCaching{b: b}

		inputs, err := rt.matchInputRoot(ctx, inputRootDigest)
		if gotErr := err != nil; gotErr != tc.wantErr {
			t.Fatalf("matchInputRoot: %v; want %v", err, tc.wantErr)
		}
		if diff := cmp.Diff(tc.wantInputs, inputs); diff != "" {
			t.Errorf("matchInputRoot: inputs -want +got:\n%s", diff)
		}
	}
}
