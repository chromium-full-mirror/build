// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/merkletree"
	"go.chromium.org/build/siso/reapi/reapitest"
)

// BenchmarkMatchInputRoot matches an input root of 4000 files in 40
// directories against the local files, after a first walk filled the
// directory caches, as for the second and later 2PC candidates.
func BenchmarkMatchInputRoot(b *testing.B) {
	ctx := b.Context()
	dir := b.TempDir()
	ds := blob.NewStore()
	tree := merkletree.New(digest.SHA256, ds)
	var want []string
	for d := range 40 {
		for f := range 100 {
			name := fmt.Sprintf("frameworks/base/dir%02d/file%03d.h", d, f)
			content := fmt.Appendf(nil, "content %d %d", d, f)
			fname := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(fname, content, 0644); err != nil {
				b.Fatal(err)
			}
			if err := tree.Set(merkletree.Entry{Name: path.Path(name), Data: blob.FromBytes(digest.SHA256, name, content)}); err != nil {
				b.Fatal(err)
			}
			want = append(want, name)
		}
	}
	inputRootDigest, err := tree.Build(ctx)
	if err != nil {
		b.Fatal(err)
	}
	reclient := reapitest.New(ctx, b, &reapitest.Fake{})
	if _, err := reclient.UploadAll(ctx, ds); err != nil {
		b.Fatal(err)
	}
	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		b.Fatal(err)
	}
	defer hashFS.Close(context.WithoutCancel(ctx))
	if err := hashFS.WaitReady(ctx); err != nil {
		b.Fatal(err)
	}
	rt := reapiTwoPhaseCaching{b: &Builder{
		path:        NewPath(dir, "out/siso"),
		hashFS:      hashFS,
		reapiclient: reclient,
	}}
	inputs, err := rt.matchInputRoot(ctx, inputRootDigest)
	if err != nil {
		b.Fatal(err)
	}
	if len(inputs) != len(want) {
		b.Fatalf("matchInputRoot returned %d inputs; want %d", len(inputs), len(want))
	}
	b.ReportAllocs()
	for b.Loop() {
		_, err := rt.matchInputRoot(ctx, inputRootDigest)
		if err != nil {
			b.Fatal(err)
		}
	}
}
