// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

func TestDirSpec(t *testing.T) {
	dir := &rpb.Directory{
		Files:       []*rpb.FileNode{{Name: "f"}, {Name: "g"}},
		Directories: []*rpb.DirectoryNode{{Name: "d"}},
		Symlinks:    []*rpb.SymlinkNode{{Name: "s", Target: "f"}},
	}
	dd := digest.Digest{Hash: "0123", SizeBytes: 4}
	b := &Builder{}

	spec := b.dirSpec("a/b", dd, dir)
	want := []string{"a/b/f", "a/b/g", "a/b/d", "a/b/s"}
	if diff := cmp.Diff(want, spec.names); diff != "" {
		t.Errorf("dirSpec(%q).names diff -want +got:\n%s", "a/b", diff)
	}
	if got := b.dirSpec("a/b", dd, dir); got != spec {
		t.Errorf("dirSpec(%q, %v) was not shared", "a/b", dd)
	}
	if got := b.dirSpec("a/c", dd, dir); got == spec {
		t.Errorf("dirSpec(%q, %v) shared with %q", "a/c", dd, "a/b")
	}

	root := b.dirSpec("", dd, dir)
	if diff := cmp.Diff([]string{"f", "g", "d", "s"}, root.names); diff != "" {
		t.Errorf("dirSpec(%q).names diff -want +got:\n%s", "", diff)
	}

	// Without a digest there is no key, so nothing is shared.
	s1 := b.dirSpec("a/b", digest.Digest{}, dir)
	s2 := b.dirSpec("a/b", digest.Digest{}, dir)
	if s1 == s2 {
		t.Errorf("dirSpec with zero digest was shared")
	}
}
