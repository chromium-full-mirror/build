// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs_test

import (
	"errors"
	"io/fs"
	"testing"

	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/reapi/digest"
	"go.chromium.org/build/siso/reapi/merkletree"
)

// TestStat_BuildWithoutBytesDirOutput locks in the build-without-bytes contract
// for directory outputs and its discriminator. A remote-only dir output
// (recorded from an action: cmdhash + action digest, IsLocal=false) lives in
// CAS, not on disk, so Stat reports it present even when absent locally. A
// directory recorded without an action digest is not CAS-backed, so Stat
// reports it missing when absent. That second case is what keeps
// StatIfExists's vanished-dir detection (TestStatIfExists_DirVanished) working,
// so the two tests pin both sides of the same condition.
func TestStat_BuildWithoutBytesDirOutput(t *testing.T) {
	ctx := t.Context()
	const name = "out/siso/gen"
	cmdhash := []byte("dircmdhash")

	// setup records "out/siso/gen" as a remote-only directory output (never on
	// disk) and returns the hashfs and its root. IsLocal defaults to false.
	setup := func(t *testing.T, action digest.Digest) (*hashfs.HashFS, string) {
		t.Helper()
		dir := t.TempDir()
		hfs, err := hashfs.New(ctx, hashfs.Option{})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() {
			if err := hfs.Close(ctx); err != nil {
				t.Errorf("hfs.Close=%v", err)
			}
		})
		if err := hfs.Update(ctx, dir, []hashfs.UpdateEntry{{
			Name:    name,
			Entry:   &merkletree.Entry{Name: name},
			Mode:    0o755 | fs.ModeDir,
			CmdHash: cmdhash,
			Action:  action,
		}}); err != nil {
			t.Fatalf("Update(%q): %v", name, err)
		}
		return hfs, dir
	}

	t.Run("with action digest reports present", func(t *testing.T) {
		hfs, dir := setup(t, digest.Digest{Hash: "actionhash", SizeBytes: 10})
		fi, err := hfs.Stat(ctx, dir, name)
		if err != nil {
			t.Fatalf("Stat(%q)=_, %v; want nil err (build-without-bytes dir output is present in CAS)", name, err)
		}
		if !fi.IsDir() {
			t.Errorf("Stat(%q).IsDir()=false; want true", name)
		}
	})

	t.Run("without action digest reports missing", func(t *testing.T) {
		hfs, dir := setup(t, digest.Digest{})
		if _, err := hfs.Stat(ctx, dir, name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Stat(%q)=_, %v; want %v (no action digest is not CAS-backed)", name, err, fs.ErrNotExist)
		}
	})
}
