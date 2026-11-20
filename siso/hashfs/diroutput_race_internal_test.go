// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/merkletree"
)

// TestShouldKeep_DiskMissDoesNotEvictBuildWithoutBytes checks that a
// local-disk miss does not evict a recorded not-local output (directory and
// its members, plain file, symlink), while an explicit removal still does.
func TestShouldKeep_DiskMissDoesNotEvictBuildWithoutBytes(t *testing.T) {
	const dir = "out/siso/gen"
	const member = "out/siso/gen/inner.txt"
	const file = "out/siso/gen/out.o"
	const link = "out/siso/gen/out.link"
	cmdhash := []byte("dircmdhash")
	action := digest.Digest{Hash: "actionhash", SizeBytes: 10}

	// record stores a not-local dir output plus one member, a plain file and a
	// symlink, like RecordOutputs of remote outputs with output_local=false.
	record := func(ctx context.Context, t *testing.T) (*HashFS, string) {
		t.Helper()
		root := t.TempDir()
		hfs, err := New(ctx, Option{})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() {
			if err := hfs.Close(ctx); err != nil {
				t.Errorf("hfs.Close=%v; want nil", err)
			}
		})
		if err := hfs.Update(ctx, root, []UpdateEntry{
			{Name: dir, Entry: &merkletree.Entry{Name: dir}, Mode: 0o755 | fs.ModeDir, CmdHash: cmdhash, Action: action},
			{Name: member, Entry: &merkletree.Entry{Name: member, Data: blob.NewData(nil, digest.Digest{Hash: "innerhash", SizeBytes: 3})}, Mode: 0o644, CmdHash: cmdhash, Action: action},
			{Name: file, Entry: &merkletree.Entry{Name: file, Data: blob.NewData(nil, digest.Digest{Hash: "filehash", SizeBytes: 4})}, Mode: 0o644, CmdHash: cmdhash, Action: action},
			{Name: link, Entry: &merkletree.Entry{Name: link, Target: "out.o"}, Mode: 0o644 | fs.ModeSymlink, CmdHash: cmdhash, Action: action},
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		return hfs, root
	}

	// negEntry builds a negative (ErrNotExist) entry. storeProbe stores it as
	// a local-disk probe; store stores it as an explicit Remove/RemoveAll.
	negEntry := func() *entry {
		e := newLocalEntry()
		e.err = fs.ErrNotExist
		return e
	}

	present := func(ctx context.Context, t *testing.T, hfs *HashFS, root string, name path.Path) bool {
		t.Helper()
		_, err := hfs.Stat(ctx, root, name)
		return err == nil
	}

	presentDir := func(ctx context.Context, t *testing.T, hfs *HashFS, root string, name path.Path) bool {
		t.Helper()
		fi, err := hfs.Stat(ctx, root, name)
		return err == nil && fi.IsDir()
	}

	t.Run("disk_miss_keeps_dir_and_member", func(t *testing.T) {
		ctx := t.Context()
		hfs, root := record(ctx, t)
		if !presentDir(ctx, t, hfs, root, dir) {
			t.Fatalf("dir %q not recorded; want present before the disk probe", dir)
		}
		if !present(ctx, t, hfs, root, member) {
			t.Fatalf("member %q not recorded; want present before the disk probe", member)
		}
		if _, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, dir), negEntry()); err != nil {
			t.Fatalf("storeProbe negative: %v", err)
		}
		if !presentDir(ctx, t, hfs, root, dir) {
			t.Errorf("Stat(%q) missing or not a dir; want present dir (disk miss must not evict a build-without-bytes dir)", dir)
		}
		if !present(ctx, t, hfs, root, member) {
			t.Errorf("member %q evicted with the dir; want preserved", member)
		}
	})

	t.Run("disk_miss_keeps_file", func(t *testing.T) {
		ctx := t.Context()
		hfs, root := record(ctx, t)
		if !present(ctx, t, hfs, root, file) {
			t.Fatalf("file %q not recorded; want present before the disk probe", file)
		}
		if _, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, file), negEntry()); err != nil {
			t.Fatalf("storeProbe negative: %v", err)
		}
		if !present(ctx, t, hfs, root, file) {
			t.Errorf("Stat(%q) missing; want present (disk miss must not evict a build-without-bytes file)", file)
		}
	})

	t.Run("disk_miss_keeps_symlink", func(t *testing.T) {
		ctx := t.Context()
		hfs, root := record(ctx, t)
		if !present(ctx, t, hfs, root, link) {
			t.Fatalf("symlink %q not recorded; want present before the disk probe", link)
		}
		if _, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, link), negEntry()); err != nil {
			t.Fatalf("storeProbe negative: %v", err)
		}
		if !present(ctx, t, hfs, root, link) {
			t.Errorf("Stat(%q) missing; want present (disk miss must not evict a build-without-bytes symlink)", link)
		}
	})

	t.Run("explicit_removal_evicts", func(t *testing.T) {
		ctx := t.Context()
		hfs, root := record(ctx, t)
		if !presentDir(ctx, t, hfs, root, dir) {
			t.Fatalf("dir %q not recorded; want present before the removal", dir)
		}
		if _, _, err := hfs.directory.store(ctx, makeFullpath(root, dir), negEntry()); err != nil {
			t.Fatalf("store negative: %v", err)
		}
		if _, err := hfs.Stat(ctx, root, dir); err == nil {
			t.Errorf("Stat(%q)=nil err; want eviction for an explicit removal", dir)
		}
	})
}

// TestShouldKeep_DiskProbeDoesNotClobberRemoteOutput checks that a concurrent
// local-disk probe (e.g. ReadDir/updateDir/statNewEntry racing ForgetOutputs
// followed by Update) cannot overwrite or revert a freshly recorded remote
// output with a stale file still on disk, whether sizes differ or match.
func TestShouldKeep_DiskProbeDoesNotClobberRemoteOutput(t *testing.T) {
	tests := []struct {
		name      string
		staleBody []byte
		freshBody []byte
	}{
		{
			name:      "different_size_stale_disk",
			staleBody: []byte("old header + kythe metadata bytes"),
			freshBody: []byte("fresh header"),
		},
		{
			name:      "same_size_stale_disk",
			staleBody: []byte("old-header-1234"),
			freshBody: []byte("new-header-5678"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			root := t.TempDir()
			const rel path.Path = "out/gen/foo.mojom-shared.h"
			full := filepath.Join(root, filepath.FromSlash(string(rel)))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatalf("os.MkdirAll(%q) = %v, want nil", filepath.Dir(full), err)
			}
			if err := os.WriteFile(full, tc.staleBody, 0o644); err != nil {
				t.Fatalf("os.WriteFile(%q) = %v, want nil", full, err)
			}
			staleTime := time.Now().Add(-6 * time.Hour).Truncate(time.Second)
			if err := os.Chtimes(full, staleTime, staleTime); err != nil {
				t.Fatalf("os.Chtimes(%q) = %v, want nil", full, err)
			}

			hfs, err := New(ctx, Option{})
			if err != nil {
				t.Fatalf("New() = %v, want nil", err)
			}
			t.Cleanup(func() {
				if err := hfs.Close(context.WithoutCancel(ctx)); err != nil {
					t.Errorf("hfs.Close() = %v, want nil", err)
				}
			})

			// Simulate a concurrent statNewEntry/updateDir probe that missed
			// lookup during ForgetOutputs->Update and read the stale disk file.
			probe := newLocalEntry()
			probe.init(ctx, full, hfs.executables, hfs.OS)

			// Remote action finishes and records its fresh CAS-backed output.
			freshTime := time.Now().Truncate(time.Second)
			freshData := blob.FromBytes(digest.SHA256, string(rel), tc.freshBody)
			if err := hfs.Update(ctx, root, []UpdateEntry{{
				Name: rel,
				Entry: &merkletree.Entry{
					Name: rel,
					Data: freshData,
				},
				Mode:        0o644,
				ModTime:     freshTime,
				CmdHash:     []byte("mojo-cmdhash"),
				Action:      digest.Digest{Hash: "actionhash", SizeBytes: 147},
				UpdatedTime: freshTime,
				IsChanged:   true,
				IsLocal:     false,
			}}); err != nil {
				t.Fatalf("hfs.Update(%q) = %v, want nil", rel, err)
			}

			// The racing disk probe now calls storeProbe after Update landed.
			if _, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, rel), probe); err != nil {
				t.Fatalf("storeProbe(%q) = %v, want nil", rel, err)
			}

			fi, err := hfs.Stat(ctx, root, rel)
			if err != nil {
				t.Fatalf("hfs.Stat(%q) = %v, want nil", rel, err)
			}
			if !fi.ModTime().Equal(freshTime) {
				t.Errorf("hfs.Stat(%q).ModTime() = %v, want %v", rel, fi.ModTime(), freshTime)
			}
			if !fi.UpdatedTime().Equal(freshTime) {
				t.Errorf("hfs.Stat(%q).UpdatedTime() = %v, want %v", rel, fi.UpdatedTime(), freshTime)
			}
			if !fi.IsChanged() {
				t.Errorf("hfs.Stat(%q).IsChanged() = false, want true", rel)
			}
			ents, err := hfs.Entries(ctx, root, []path.Path{rel})
			if err != nil || len(ents) != 1 {
				t.Fatalf("hfs.Entries(%q) = (len %d, %v), want (len 1, nil)", rel, len(ents), err)
			}
			if got := ents[0].Data.Digest(); got != freshData.Digest() {
				t.Errorf("hfs.Entries(%q)[0].Data.Digest() = %v, want %v", rel, got, freshData.Digest())
			}

			if err := hfs.Flush(ctx, root, []path.Path{rel}); err != nil {
				t.Fatalf("hfs.Flush(%q) = %v, want nil", rel, err)
			}
			onDisk, err := os.ReadFile(full)
			if err != nil {
				t.Fatalf("os.ReadFile(%q) = %v, want nil", full, err)
			}
			if !bytes.Equal(onDisk, tc.freshBody) {
				t.Errorf("os.ReadFile(%q) = %q, want %q", full, onDisk, tc.freshBody)
			}
		})
	}
}

func TestShouldKeep_DiskProbeDoesNotResurrectRemoval(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	const fileRel path.Path = "out/gen/removed.h"
	const dirRel path.Path = "out/removed_dir"
	const nestedRel path.Path = "out/removed_dir/child.h"
	for _, rel := range []path.Path{fileRel, nestedRel} {
		full := filepath.Join(root, filepath.FromSlash(string(rel)))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) = %v, want nil", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte("stale"), 0o644); err != nil {
			t.Fatalf("os.WriteFile(%q) = %v, want nil", full, err)
		}
	}

	hfs, err := New(ctx, Option{})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := hfs.Close(context.WithoutCancel(ctx)); err != nil {
			t.Errorf("hfs.Close() = %v, want nil", err)
		}
	})

	fileProbe := newLocalEntry()
	fileProbe.init(ctx, filepath.Join(root, filepath.FromSlash(string(fileRel))), hfs.executables, hfs.OS)
	nestedProbe := newLocalEntry()
	nestedProbe.init(ctx, filepath.Join(root, filepath.FromSlash(string(nestedRel))), hfs.executables, hfs.OS)

	if err := hfs.Remove(ctx, root, fileRel); err != nil {
		t.Fatalf("hfs.Remove(%q) = %v, want nil", fileRel, err)
	}
	if err := hfs.RemoveAll(ctx, root, dirRel); err != nil {
		t.Fatalf("hfs.RemoveAll(%q) = %v, want nil", dirRel, err)
	}

	gotFile, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, fileRel), fileProbe)
	if err != nil {
		t.Fatalf("storeProbe(%q) = %v, want nil", fileRel, err)
	}
	if !errors.Is(gotFile.getErr(), fs.ErrNotExist) {
		t.Errorf("storeProbe(%q).getErr() = %v, want %v", fileRel, gotFile.getErr(), fs.ErrNotExist)
	}
	if _, err := hfs.Stat(ctx, root, fileRel); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("hfs.Stat(%q) err = %v, want %v", fileRel, err, fs.ErrNotExist)
	}

	gotNested, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, nestedRel), nestedProbe)
	if err != nil {
		t.Fatalf("storeProbe(%q) = %v, want nil", nestedRel, err)
	}
	if !errors.Is(gotNested.getErr(), fs.ErrNotExist) {
		t.Errorf("storeProbe(%q).getErr() = %v, want %v", nestedRel, gotNested.getErr(), fs.ErrNotExist)
	}
	if _, err := hfs.Stat(ctx, root, dirRel); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("hfs.Stat(%q) err = %v, want %v", dirRel, err, fs.ErrNotExist)
	}

	// Non-ErrNotExist error on an intermediate directory is not a removal
	// tombstone, so a probe below it still resolves the directory on disk.
	const permDirRel path.Path = "out/perm_dir"
	const permFileRel path.Path = "out/perm_dir/b.h"
	permFull := filepath.Join(root, filepath.FromSlash(string(permFileRel)))
	if err := os.MkdirAll(filepath.Dir(permFull), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) = %v, want nil", filepath.Dir(permFull), err)
	}
	if err := os.WriteFile(permFull, []byte("ok"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v, want nil", permFull, err)
	}
	permErrEntry := newLocalEntry()
	permErrEntry.err = fs.ErrPermission
	if _, _, err := hfs.directory.store(ctx, makeFullpath(root, permDirRel), permErrEntry); err != nil {
		t.Fatalf("store(%q) = %v, want nil", permDirRel, err)
	}
	if _, err := hfs.Stat(ctx, root, permFileRel); err != nil {
		t.Errorf("hfs.Stat(%q) = %v, want nil", permFileRel, err)
	}

	// Explicit Remove/RemoveAll over a prior negative disk probe must promote
	// it to an authoritative tombstone that later probes cannot resurrect.
	const probedFileRel path.Path = "out/gen/probed_removed.h"
	const probedDirRel path.Path = "out/probed_removed_dir"
	const probedNestedRel path.Path = "out/probed_removed_dir/child.h"
	negFile := newLocalEntry()
	negFile.init(ctx, filepath.Join(root, filepath.FromSlash(string(probedFileRel))), hfs.executables, hfs.OS)
	if _, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, probedFileRel), negFile); err != nil {
		t.Fatalf("storeProbe(neg %q) = %v, want nil", probedFileRel, err)
	}
	negDir := newLocalEntry()
	negDir.init(ctx, filepath.Join(root, filepath.FromSlash(string(probedDirRel))), hfs.executables, hfs.OS)
	if _, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, probedDirRel), negDir); err != nil {
		t.Fatalf("storeProbe(neg %q) = %v, want nil", probedDirRel, err)
	}
	if err := hfs.Remove(ctx, root, probedFileRel); err != nil {
		t.Fatalf("hfs.Remove(%q) = %v, want nil", probedFileRel, err)
	}
	if err := hfs.RemoveAll(ctx, root, probedDirRel); err != nil {
		t.Fatalf("hfs.RemoveAll(%q) = %v, want nil", probedDirRel, err)
	}
	for _, rel := range []path.Path{probedFileRel, probedNestedRel} {
		full := filepath.Join(root, filepath.FromSlash(string(rel)))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) = %v, want nil", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte("stale"), 0o644); err != nil {
			t.Fatalf("os.WriteFile(%q) = %v, want nil", full, err)
		}
	}
	posFile := newLocalEntry()
	posFile.init(ctx, filepath.Join(root, filepath.FromSlash(string(probedFileRel))), hfs.executables, hfs.OS)
	gotProbedFile, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, probedFileRel), posFile)
	if err != nil {
		t.Fatalf("storeProbe(%q) = %v, want nil", probedFileRel, err)
	}
	if !errors.Is(gotProbedFile.getErr(), fs.ErrNotExist) {
		t.Errorf("storeProbe(%q).getErr() = %v, want %v", probedFileRel, gotProbedFile.getErr(), fs.ErrNotExist)
	}
	// Flush must also remove probedFileRel from disk (requires Remove's
	// buffered lready to replace the negative probe's closed lready).
	if err := hfs.Flush(ctx, root, []path.Path{probedFileRel}); err != nil {
		t.Fatalf("hfs.Flush(%q) = %v, want nil", probedFileRel, err)
	}
	probedFileFull := filepath.Join(root, filepath.FromSlash(string(probedFileRel)))
	if _, err := os.Lstat(probedFileFull); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("os.Lstat(%q) err = %v, want %v after Flush", probedFileFull, err, fs.ErrNotExist)
	}
	posNested := newLocalEntry()
	posNested.init(ctx, filepath.Join(root, filepath.FromSlash(string(probedNestedRel))), hfs.executables, hfs.OS)
	gotProbedNested, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, probedNestedRel), posNested)
	if err != nil {
		t.Fatalf("storeProbe(%q) = %v, want nil", probedNestedRel, err)
	}
	if !errors.Is(gotProbedNested.getErr(), fs.ErrNotExist) {
		t.Errorf("storeProbe(%q).getErr() = %v, want %v", probedNestedRel, gotProbedNested.getErr(), fs.ErrNotExist)
	}

	// Remove on a leaf below a missing parent caches an ancestor disk miss, not
	// a removal tombstone on that parent, so a positive probe below it succeeds.
	const missingParentLeaf path.Path = "out/missing_parent/removed.h"
	const missingParentOther path.Path = "out/missing_parent/other.h"
	if err := hfs.Remove(ctx, root, missingParentLeaf); err != nil {
		t.Fatalf("hfs.Remove(%q) = %v, want nil", missingParentLeaf, err)
	}
	otherFull := filepath.Join(root, filepath.FromSlash(string(missingParentOther)))
	if err := os.MkdirAll(filepath.Dir(otherFull), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) = %v, want nil", filepath.Dir(otherFull), err)
	}
	if err := os.WriteFile(otherFull, []byte("hello"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v, want nil", otherFull, err)
	}
	otherProbe := newLocalEntry()
	otherProbe.init(ctx, otherFull, hfs.executables, hfs.OS)
	gotOther, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, missingParentOther), otherProbe)
	if err != nil {
		t.Fatalf("storeProbe(%q) = %v, want nil", missingParentOther, err)
	}
	if gotOther != otherProbe {
		t.Errorf("storeProbe(%q) returned %p, want %p", missingParentOther, gotOther, otherProbe)
	}
	if _, err := hfs.Stat(ctx, root, missingParentOther); err != nil {
		t.Errorf("hfs.Stat(%q) = %v, want nil", missingParentOther, err)
	}
}

func TestShouldKeep_PositiveDiskProbeReplacesNegativeDiskProbe(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	const rel path.Path = "out/gen/appeared.h"
	full := filepath.Join(root, filepath.FromSlash(string(rel)))
	// Create the parent directory before the negative probe so storeProbe(neg)
	// caches neg at the leaf rather than short-circuiting on a missing ancestor.
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) = %v, want nil", filepath.Dir(full), err)
	}

	hfs, err := New(ctx, Option{})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := hfs.Close(context.WithoutCancel(ctx)); err != nil {
			t.Errorf("hfs.Close() = %v, want nil", err)
		}
	})

	neg := newLocalEntry()
	neg.init(ctx, full, hfs.executables, hfs.OS)
	gotNeg, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, rel), neg)
	if err != nil {
		t.Fatalf("storeProbe(neg %q) = %v, want nil", rel, err)
	}
	if gotNeg != neg {
		t.Fatalf("storeProbe(neg %q) returned %p, want leaf entry %p", rel, gotNeg, neg)
	}

	if err := os.WriteFile(full, []byte("hello"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v, want nil", full, err)
	}

	pos := newLocalEntry()
	pos.init(ctx, full, hfs.executables, hfs.OS)
	gotPos, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, rel), pos)
	if err != nil {
		t.Fatalf("storeProbe(pos %q) = %v, want nil", rel, err)
	}
	if gotPos != pos {
		t.Errorf("storeProbe(pos %q) returned %p, want %p", rel, gotPos, pos)
	}
	fi, err := hfs.Stat(ctx, root, rel)
	if err != nil {
		t.Fatalf("hfs.Stat(%q) = %v, want nil", rel, err)
	}
	if fi.Size() != 5 {
		t.Errorf("hfs.Stat(%q).Size() = %d, want 5", rel, fi.Size())
	}
}

func TestStoreNextDir_PositiveDiskProbeReplacesNegativeIntermediateDir(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	const rel path.Path = "out/gen/appeared.h"
	full := filepath.Join(root, filepath.FromSlash(string(rel)))

	hfs, err := New(ctx, Option{})
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := hfs.Close(context.WithoutCancel(ctx)); err != nil {
			t.Errorf("hfs.Close() = %v, want nil", err)
		}
	})

	neg := newLocalEntry()
	neg.init(ctx, full, hfs.executables, hfs.OS)
	gotNeg, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, rel), neg)
	if err != nil {
		t.Fatalf("storeProbe(neg %q) = %v, want nil", rel, err)
	}
	if gotNeg != missingEntry {
		t.Fatalf("storeProbe(neg %q) returned %p, want missingEntry %p", rel, gotNeg, missingEntry)
	}
	if _, err := hfs.Stat(ctx, root, "out"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("hfs.Stat(%q) err = %v, want %v", "out", err, fs.ErrNotExist)
	}

	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) = %v, want nil", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte("hello"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) = %v, want nil", full, err)
	}

	pos := newLocalEntry()
	pos.init(ctx, full, hfs.executables, hfs.OS)
	gotPos, _, err := hfs.directory.storeProbe(ctx, makeFullpath(root, rel), pos)
	if err != nil {
		t.Fatalf("storeProbe(pos %q) = %v, want nil", rel, err)
	}
	if gotPos != pos {
		t.Errorf("storeProbe(pos %q) returned %p, want %p", rel, gotPos, pos)
	}
	fi, err := hfs.Stat(ctx, root, rel)
	if err != nil {
		t.Fatalf("hfs.Stat(%q) = %v, want nil", rel, err)
	}
	if fi.Size() != 5 {
		t.Errorf("hfs.Stat(%q).Size() = %d, want 5", rel, fi.Size())
	}
}
