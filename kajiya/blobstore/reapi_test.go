// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// TestFlattenDirectoryMissingSubdirectories verifies that FlattenDirectory
// returns the part of a tree that is in the CAS along with a
// *MissingBlobsError naming every missing subdirectory, and returns nothing
// but the error if the root is missing.
func TestFlattenDirectoryMissingSubdirectories(t *testing.T) {
	// Create an empty CAS.
	cas, err := NewWithOpts(t.Context(), t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("NewWithOpts: %v", err)
	}

	// Store a root that references two subdirectories missing from the CAS,
	// one of them twice.
	lost1 := digest.SHA256.FromBytes([]byte("never stored 1"))
	lost2 := digest.SHA256.FromBytes([]byte("never stored 2"))
	root := &repb.Directory{Directories: []*repb.DirectoryNode{
		{Name: "lost1", Digest: lost1.Proto()},
		{Name: "lost1_copy", Digest: lost1.Proto()},
		{Name: "lost2", Digest: lost2.Proto()},
	}}
	rootBytes, err := proto.Marshal(root)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	rootDigest, err := cas.Put(digest.SHA256, rootBytes)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Check that FlattenDirectory returns the root and names each missing
	// subdirectory once.
	gotDigests, gotDirs, err := cas.FlattenDirectory(digest.SHA256, rootDigest)
	mbe, ok := errors.AsType[*MissingBlobsError](err)
	if !ok {
		t.Fatalf("FlattenDirectory: err = %v, want a *MissingBlobsError", err)
	}
	if diff := cmp.Diff([]digest.Digest{lost1, lost2}, mbe.Blobs); diff != "" {
		t.Errorf("FlattenDirectory: missing blobs mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]digest.Digest{rootDigest}, gotDigests); diff != "" {
		t.Errorf("FlattenDirectory: digests mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]*repb.Directory{root}, gotDirs, protocmp.Transform()); diff != "" {
		t.Errorf("FlattenDirectory: directories mismatch (-want +got):\n%s", diff)
	}

	// Check that a missing root yields no directories.
	gotDigests, gotDirs, err = cas.FlattenDirectory(digest.SHA256, lost1)
	if _, ok := errors.AsType[*MissingBlobsError](err); !ok {
		t.Errorf("FlattenDirectory with a missing root: err = %v, want a *MissingBlobsError", err)
	}
	if len(gotDigests) != 0 || len(gotDirs) != 0 {
		t.Errorf("FlattenDirectory with a missing root returned %d digests and %d directories, want none", len(gotDigests), len(gotDirs))
	}
}
