// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/blob"
	pb "go.chromium.org/build/siso/hashfs/proto"
	"go.chromium.org/build/siso/path"
)

func TestEntryKind(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ent      *pb.Entry
		wantKind pb.Entry_Kind
		wantSize int64
	}{
		{
			name:     "legacy_file",
			ent:      &pb.Entry{Digest: &pb.Digest{Hash: "abc", SizeBytes: 3}},
			wantKind: pb.Entry_KIND_FILE,
			wantSize: 3,
		},
		{
			name:     "legacy_symlink",
			ent:      &pb.Entry{Target: "foo"},
			wantKind: pb.Entry_KIND_SYMLINK,
		},
		{
			name:     "legacy_dir",
			ent:      &pb.Entry{CmdHash: []byte("cmd")},
			wantKind: pb.Entry_KIND_DIR,
		},
		{
			name:     "file_without_digest",
			ent:      &pb.Entry{Kind: pb.Entry_KIND_FILE, Size: 42},
			wantKind: pb.Entry_KIND_FILE,
			wantSize: 42,
		},
		{
			name:     "file_with_digest",
			ent:      &pb.Entry{Kind: pb.Entry_KIND_FILE, Digest: &pb.Digest{Hash: "abc", SizeBytes: 3}},
			wantKind: pb.Entry_KIND_FILE,
			wantSize: 3,
		},
		{
			name:     "dir",
			ent:      &pb.Entry{Kind: pb.Entry_KIND_DIR},
			wantKind: pb.Entry_KIND_DIR,
		},
		{
			name:     "symlink",
			ent:      &pb.Entry{Kind: pb.Entry_KIND_SYMLINK, Target: "foo"},
			wantKind: pb.Entry_KIND_SYMLINK,
		},
		{
			name:     "unknown_kind_falls_back_to_inference",
			ent:      &pb.Entry{Kind: pb.Entry_Kind(99), Digest: &pb.Digest{Hash: "abc", SizeBytes: 3}},
			wantKind: pb.Entry_KIND_FILE,
			wantSize: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := EntryKind(tc.ent), tc.wantKind; got != want {
				t.Errorf("EntryKind(%v) = %v; want %v", tc.ent, got, want)
			}
			if got, want := EntrySize(tc.ent), tc.wantSize; got != want {
				t.Errorf("EntrySize(%v) = %d; want %d", tc.ent, got, want)
			}
		})
	}
}

// mtimeOnlyFixture is a workspace with one file that the build statted, but
// never read.
type mtimeOnlyFixture struct {
	dir       string
	stateFile string
	src       string
	content   string
	past      time.Time
	targets   []string
}

func newMtimeOnlyFixture(t *testing.T, mode fs.FileMode) *mtimeOnlyFixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &mtimeOnlyFixture{
		dir:       dir,
		stateFile: filepath.Join(dir, ".siso_fs_state"),
		src:       filepath.ToSlash(filepath.Join(dir, "tool.py")),
		content:   "print('hello from mtime-only file')\n",
		past:      time.Now().Add(-2 * time.Second),
		targets:   []string{"all"},
	}
	if err := os.WriteFile(f.src, []byte(f.content), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile doesn't change the mode of an existing file.
	if err := os.Chmod(f.src, mode); err != nil {
		t.Fatal(err)
	}
	f.setMtime(t, f.past)
	return f
}

func (f *mtimeOnlyFixture) setMtime(t *testing.T, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(f.src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// build runs a build that only stats the file, and saves the state.
// The state file is removed first, since a clean load skips the save.
func (f *mtimeOnlyFixture) build(t *testing.T, opt Option, setup func(*HashFS)) {
	t.Helper()
	if err := os.Remove(f.stateFile); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	opt.StateFile = f.stateFile
	hfs, err := New(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(hfs)
	}
	if _, err := hfs.StatMtime(t.Context(), f.dir, "tool.py"); err != nil {
		t.Fatal(err)
	}
	hfs.SetBuildTargets(t.Context(), f.targets, true)
	if err := hfs.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// load starts the next build and waits until the state is loaded.
func (f *mtimeOnlyFixture) load(t *testing.T, opt Option) *HashFS {
	t.Helper()
	opt.StateFile = f.stateFile
	ctx := t.Context()
	cleanupCtx := context.WithoutCancel(ctx)
	hfs, err := New(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hfs.Close(cleanupCtx) })
	if err := hfs.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	return hfs
}

func (f *mtimeOnlyFixture) savedEntry(t *testing.T) *pb.Entry {
	t.Helper()
	st, err := Load(t.Context(), Option{StateFile: f.stateFile})
	if err != nil {
		t.Fatal(err)
	}
	return StateMap(digest.SHA256, st)[f.src]
}

func (f *mtimeOnlyFixture) lookup(t *testing.T, hfs *HashFS) *entry {
	t.Helper()
	e, _, _, ok := hfs.directory.lookup(t.Context(), path.FromClean(f.src))
	if !ok || e == nil {
		t.Fatalf("%s not in hashfs after load (ok=%t)", f.src, ok)
	}
	return e
}

// TestState_MtimeOnly checks that a file that was statted, but never read,
// is saved as a file without digest, and that the next load finds it clean
// or dirty by mtime, type and size.
func TestState_MtimeOnly(t *testing.T) {
	f := newMtimeOnlyFixture(t, 0755)
	f.build(t, Option{}, nil)

	ent := f.savedEntry(t)
	if ent == nil {
		t.Fatalf("state has no entry for %s", f.src)
	}
	// Windows has no execute permission bits on disk.
	wantExec := runtime.GOOS != "windows"
	wantMode := fs.FileMode(0644)
	if wantExec {
		wantMode = 0755
	}
	want := &pb.Entry{
		Id:           &pb.FileID{ModTime: f.past.UnixNano()},
		Name:         f.src,
		IsExecutable: wantExec,
		UpdatedTime:  f.past.UnixNano(),
		Kind:         pb.Entry_KIND_FILE,
		Size:         int64(len(f.content)),
	}
	if !proto.Equal(ent, want) {
		t.Errorf("saved entry = %v; want %v", ent, want)
	}
	st, err := Load(t.Context(), Option{StateFile: f.stateFile})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.MissingDigests) != 0 {
		t.Errorf("missing_digests = %q; want empty", st.MissingDigests)
	}

	t.Run("unchanged", func(t *testing.T) {
		hfs := f.load(t, Option{})
		if got, want := hfs.IsClean(f.targets), true; got != want {
			t.Errorf("IsClean = %t; want %t", got, want)
		}
		e := f.lookup(t, hfs)
		if got, want := e.getMtime(), f.past; !got.Equal(want) {
			t.Errorf("mtime = %v; want %v", got, want)
		}
		if got, want := e.size, int64(len(f.content)); got != want {
			t.Errorf("size = %d; want %d", got, want)
		}
		if got, want := e.mode, wantMode; got != want {
			t.Errorf("mode = %v; want %v", got, want)
		}
		if d := e.digest(); !d.IsZero() {
			t.Errorf("digest = %v; want zero until the file is read", d)
		}
		// The next save keeps the entry, even if this build didn't stat it.
		if got := StateMap(digest.SHA256, hfs.State(t.Context()))[f.src]; !proto.Equal(got, want) {
			t.Errorf("entry in next state = %v; want %v", got, want)
		}
		buf, err := hfs.ReadFile(t.Context(), f.dir, "tool.py")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(buf), f.content; got != want {
			t.Errorf("ReadFile = %q; want %q", got, want)
		}
	})

	for _, tc := range []struct {
		name   string
		modify func(t *testing.T)
	}{
		{
			name: "touched",
			modify: func(t *testing.T) {
				f.setMtime(t, f.past.Add(time.Second))
			},
		},
		{
			name: "backdated",
			modify: func(t *testing.T) {
				f.setMtime(t, f.past.Add(-time.Second))
			},
		},
		{
			name: "size_changed_same_mtime",
			modify: func(t *testing.T) {
				if err := os.WriteFile(f.src, []byte(f.content+"# more\n"), 0755); err != nil {
					t.Fatal(err)
				}
				f.setMtime(t, f.past)
			},
		},
		{
			name: "removed",
			modify: func(t *testing.T) {
				if err := os.Remove(f.src); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "replaced_by_dir_same_mtime",
			modify: func(t *testing.T) {
				if err := os.Remove(f.src); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.src, 0755); err != nil {
					t.Fatal(err)
				}
				f.setMtime(t, f.past)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() {
				if err := os.RemoveAll(f.src); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.src, []byte(f.content), 0755); err != nil {
					t.Fatal(err)
				}
				f.setMtime(t, f.past)
			})
			f.build(t, Option{}, nil)
			tc.modify(t)
			hfs := f.load(t, Option{})
			if got, want := hfs.IsClean(f.targets), false; got != want {
				t.Errorf("IsClean = %t; want %t", got, want)
			}
		})
	}
}

// TestState_MtimeOnly_Executables checks that the executable bit from the
// step config is saved, so that it doesn't depend on whether
// SetExecutables runs before or after the loaded entries are stored.
func TestState_MtimeOnly_Executables(t *testing.T) {
	f := newMtimeOnlyFixture(t, 0644)
	f.build(t, Option{}, func(hfs *HashFS) {
		hfs.SetExecutables(t.Context(), map[path.Path]bool{path.FromClean(f.src): true})
	})
	if ent := f.savedEntry(t); !ent.GetIsExecutable() {
		t.Errorf("saved entry = %v; want is_executable", ent)
	}

	// The next build never calls SetExecutables.
	hfs := f.load(t, Option{})
	if got, want := hfs.IsClean(f.targets), true; got != want {
		t.Errorf("IsClean = %t; want %t", got, want)
	}
	if got, want := f.lookup(t, hfs).mode&0111, fs.FileMode(0111); got != want {
		t.Errorf("mode&0111 = %#o; want %#o", got, want)
	}
}

// TestState_MtimeOnly_OldFormat checks compatibility with older siso.
func TestState_MtimeOnly_OldFormat(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(st *pb.State, ent *pb.Entry)
	}{
		{
			// Older siso saved the name only in missing_digests.
			name: "missing_digests",
			edit: func(st *pb.State, ent *pb.Entry) {
				st.Entries = nil
				st.MissingDigests = append(st.MissingDigests, ent.Name)
			},
		},
		{
			// Without kind, the entry is read as older siso reads
			// it: as a directory, which doesn't match the file on
			// disk.
			name: "older_reader",
			edit: func(_ *pb.State, ent *pb.Entry) {
				ent.Kind = pb.Entry_KIND_UNSPECIFIED
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMtimeOnlyFixture(t, 0644)
			f.build(t, Option{}, nil)
			st, err := Load(t.Context(), Option{StateFile: f.stateFile})
			if err != nil {
				t.Fatal(err)
			}
			ent := StateMap(digest.SHA256, st)[f.src]
			if ent == nil {
				t.Fatalf("state has no entry for %s", f.src)
			}
			tc.edit(st, ent)
			if err := Save(t.Context(), st, Option{StateFile: f.stateFile}); err != nil {
				t.Fatal(err)
			}
			hfs := f.load(t, Option{})
			if got, want := hfs.IsClean(f.targets), false; got != want {
				t.Errorf("IsClean = %t; want %t", got, want)
			}
		})
	}
}

// TestState_MtimeOnly_Journal checks that a crashed build that wrote a file
// as a step output wins over the file's mtime-only entry in the state.
func TestState_MtimeOnly_Journal(t *testing.T) {
	for i := range 20 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f := newMtimeOnlyFixture(t, 0644)
			f.build(t, Option{}, nil)

			// The next build writes the file as a step output and crashes:
			// the journal is not merged into the state file.
			out := []byte("generated\n")
			cmdhash := []byte("cmdhash")
			ctx := t.Context()
			cleanupCtx := context.WithoutCancel(ctx)
			crashed, err := New(ctx, Option{StateFile: f.stateFile})
			if err != nil {
				t.Fatal(err)
			}
			// Closing it before the next load would save its state
			// and remove the journal.
			t.Cleanup(func() { crashed.Close(cleanupCtx) })
			if err := crashed.WaitReady(ctx); err != nil {
				t.Fatal(err)
			}
			if err := crashed.WriteFile(t.Context(), f.dir, "tool.py", out, false, time.Now(), cmdhash, nil); err != nil {
				t.Fatal(err)
			}

			hfs := f.load(t, Option{})
			got := StateMap(digest.SHA256, hfs.State(t.Context()))[f.src]
			wantDigest := blob.FromBytes(digest.SHA256, f.src, out).Digest()
			if got == nil || toDigest(got.Digest) != wantDigest || !bytes.Equal(got.CmdHash, cmdhash) {
				t.Errorf("entry = %v; want digest %v and cmdhash %q", got, wantDigest, cmdhash)
			}
		})
	}
}

// syntheticEntryFileInfo mimics watchmanutil.FileInfo for unmodified files.
type syntheticEntryFileInfo struct {
	ent *pb.Entry
}

func (fi syntheticEntryFileInfo) Name() string { return filepath.Base(fi.ent.Name) }
func (fi syntheticEntryFileInfo) Size() int64  { return EntrySize(fi.ent) }
func (fi syntheticEntryFileInfo) Mode() fs.FileMode {
	m := fs.FileMode(0644)
	if fi.ent.IsExecutable {
		m |= 0111
	}
	switch EntryKind(fi.ent) {
	case pb.Entry_KIND_SYMLINK:
		m |= fs.ModeSymlink
	case pb.Entry_KIND_DIR:
		m |= fs.ModeDir | 0111
	}
	return m
}
func (fi syntheticEntryFileInfo) ModTime() time.Time {
	return time.Unix(0, fi.ent.GetId().GetModTime())
}
func (fi syntheticEntryFileInfo) IsDir() bool { return EntryKind(fi.ent) == pb.Entry_KIND_DIR }
func (fi syntheticEntryFileInfo) Sys() any    { return fi.ent }

type fakeFSMonitor struct {
	token   string
	changed map[string]bool // true = exists & modified, false = deleted; absent = unmodified
}

func (m *fakeFSMonitor) ClockToken(context.Context) (string, error) {
	return m.token, nil
}

func (m *fakeFSMonitor) Scan(_ context.Context, lastChecked string) (FileInfoer, error) {
	if lastChecked != m.token {
		return nil, fmt.Errorf("unexpected token %q", lastChecked)
	}
	return m, nil
}

func (m *fakeFSMonitor) FileInfo(_ context.Context, ent *pb.Entry) (fs.FileInfo, error) {
	exists, changed := m.changed[ent.Name]
	if !changed {
		return syntheticEntryFileInfo{ent: proto.Clone(ent).(*pb.Entry)}, nil
	}
	if !exists {
		return nil, fs.ErrNotExist
	}
	return os.Lstat(ent.Name)
}

// TestState_MtimeOnly_FSMonitor checks that unmodified files without
// digest are loaded from the FSMonitor's FileInfo, without stat-ing them.
func TestState_MtimeOnly_FSMonitor(t *testing.T) {
	f := newMtimeOnlyFixture(t, 0755)
	mon := &fakeFSMonitor{token: "c:1:1", changed: map[string]bool{}}
	f.build(t, Option{FSMonitor: mon}, nil)

	t.Run("unmodified", func(t *testing.T) {
		// Move the file away while loading, so that stat-ing it makes
		// the state dirty.
		moved := f.src + ".moved"
		if err := os.Rename(f.src, moved); err != nil {
			t.Fatal(err)
		}
		hfs := f.load(t, Option{FSMonitor: mon})
		if err := os.Rename(moved, f.src); err != nil {
			t.Fatal(err)
		}
		if got, want := hfs.IsClean(f.targets), true; got != want {
			t.Errorf("IsClean = %t; want %t", got, want)
		}
		buf, err := hfs.ReadFile(t.Context(), f.dir, "tool.py")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(buf), f.content; got != want {
			t.Errorf("ReadFile = %q; want %q", got, want)
		}
	})

	t.Run("modified", func(t *testing.T) {
		f.setMtime(t, f.past.Add(time.Second))
		mon.changed[f.src] = true
		t.Cleanup(func() { delete(mon.changed, f.src) })
		hfs := f.load(t, Option{FSMonitor: mon})
		if got, want := hfs.IsClean(f.targets), false; got != want {
			t.Errorf("IsClean = %t; want %t", got, want)
		}
	})
}

// TestState_MtimeOnly_GeneratedWithoutDigest checks that an output saved
// without digest (e.g. digest calculation failed) is invalidated when the
// local file doesn't match, and that the local file is kept. Without
// digest, the output can't be fetched from remote.
func TestState_MtimeOnly_GeneratedWithoutDigest(t *testing.T) {
	f := newMtimeOnlyFixture(t, 0644)
	f.build(t, Option{}, nil)
	st, err := Load(t.Context(), Option{StateFile: f.stateFile})
	if err != nil {
		t.Fatal(err)
	}
	ent := StateMap(digest.SHA256, st)[f.src]
	if ent == nil {
		t.Fatalf("state has no entry for %s", f.src)
	}
	// Make it an output that is newer than the local file.
	ent.CmdHash = []byte("cmdhash")
	ent.Id.ModTime = f.past.Add(time.Second).UnixNano()
	if err := Save(t.Context(), st, Option{StateFile: f.stateFile}); err != nil {
		t.Fatal(err)
	}

	hfs := f.load(t, Option{})
	if got, want := hfs.IsClean(f.targets), false; got != want {
		t.Errorf("IsClean = %t; want %t", got, want)
	}
	if _, err := os.Stat(f.src); err != nil {
		t.Errorf("local file was removed: %v", err)
	}
	if _, ok := StateMap(digest.SHA256, hfs.State(t.Context()))[f.src]; ok {
		t.Errorf("entry for %s is kept; want invalidated", f.src)
	}
}
