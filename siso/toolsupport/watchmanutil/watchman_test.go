// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package watchmanutil

import (
	"io/fs"
	"testing"

	pb "go.chromium.org/build/siso/hashfs/proto"
)

func TestFileInfo(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ent      *pb.Entry
		wantDir  bool
		wantSize int64
		wantMode fs.FileMode
	}{
		{
			name:     "file",
			ent:      &pb.Entry{Digest: &pb.Digest{Hash: "abc", SizeBytes: 3}},
			wantSize: 3,
			wantMode: 0644,
		},
		{
			name:     "file_without_digest",
			ent:      &pb.Entry{Kind: pb.Entry_KIND_FILE, Size: 42, IsExecutable: true},
			wantSize: 42,
			wantMode: 0755,
		},
		{
			name:     "symlink",
			ent:      &pb.Entry{Target: "foo"},
			wantMode: 0644 | fs.ModeSymlink,
		},
		{
			name:     "legacy_dir",
			ent:      &pb.Entry{CmdHash: []byte("cmd")},
			wantDir:  true,
			wantMode: 0755 | fs.ModeDir,
		},
		{
			name:     "dir",
			ent:      &pb.Entry{Kind: pb.Entry_KIND_DIR},
			wantDir:  true,
			wantMode: 0755 | fs.ModeDir,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fi := FileInfo{ent: tc.ent}
			if got, want := fi.IsDir(), tc.wantDir; got != want {
				t.Errorf("IsDir() = %t; want %t", got, want)
			}
			if got, want := fi.Size(), tc.wantSize; got != want {
				t.Errorf("Size() = %d; want %d", got, want)
			}
			if got, want := fi.Mode(), tc.wantMode; got != want {
				t.Errorf("Mode() = %v; want %v", got, want)
			}
		})
	}
}
