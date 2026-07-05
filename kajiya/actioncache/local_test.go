// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package actioncache

import (
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/digest"
)

func TestValidateActionOutputDirectories(t *testing.T) {
	cas, err := blobstore.NewWithOpts(t.Context(), t.TempDir(), blobstore.Options{})
	if err != nil {
		t.Fatalf("blobstore.NewWithOpts: %v", err)
	}

	// Store an output file and a Directory referencing it in the CAS.
	fileDigest, err := cas.Put([]byte("output file content"))
	if err != nil {
		t.Fatalf("cas.Put(file): %v", err)
	}
	dirBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(&repb.Directory{
		Files: []*repb.FileNode{{Name: "f", Digest: fileDigest.ToProto()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dirDigest, err := cas.Put(dirBytes)
	if err != nil {
		t.Fatalf("cas.Put(directory): %v", err)
	}

	ac, err := NewWithOpts(t.Context(), t.TempDir(), cas, Options{})
	if err != nil {
		t.Fatalf("NewWithOpts: %v", err)
	}
	actionDigest := digest.FromBlob([]byte("some action"))
	if err := ac.Put(actionDigest, &repb.ActionResult{
		OutputDirectories: []*repb.OutputDirectory{{Path: "out", RootDirectoryDigest: dirDigest.ToProto()}},
	}); err != nil {
		t.Fatalf("ac.Put: %v", err)
	}

	blobs, err := ac.validateAction(actionDigest)
	if err != nil {
		t.Fatalf("validateAction: %v", err)
	}
	for _, want := range []digest.Digest{dirDigest, fileDigest} {
		if !slices.Contains(blobs, want) {
			t.Errorf("validateAction blobs = %v, want to contain %s", blobs, want)
		}
	}
}
