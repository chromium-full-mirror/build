// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	pb "go.chromium.org/build/siso/hashfs/proto"
)

func setupGCFileSystem(t *testing.T) (*HashFS, string, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	hfs := &HashFS{
		buildLabelDictionary: make(map[uint32]*pb.BuildLabelMetadata),
		fileBuildLabels:      make(map[string]uint64),
	}

	now := time.Now().UnixNano()

	hfs.buildLabelDictionary[0] = &pb.BuildLabelMetadata{BuildLabel: "chromeos", LastBuildTimestamp: now - time.Hour.Nanoseconds()}
	hfs.buildLabelDictionary[1] = &pb.BuildLabelMetadata{BuildLabel: "android", LastBuildTimestamp: now - time.Second.Nanoseconds()}
	hfs.buildLabelDictionary[2] = &pb.BuildLabelMetadata{BuildLabel: "pixel", LastBuildTimestamp: now - (24 * time.Hour).Nanoseconds()}

	fileA := filepath.Join(dir, "chromeos_exclusive")
	fileB := filepath.Join(dir, "android_exclusive")
	fileC := filepath.Join(dir, "pixel_exclusive")
	fileShared := filepath.Join(dir, "shared_bin")

	os.WriteFile(fileA, []byte("111"), 0644)
	os.WriteFile(fileB, []byte("2222"), 0644)
	os.WriteFile(fileC, []byte("33333"), 0644)
	os.WriteFile(fileShared, []byte("999999"), 0644)

	hfs.fileBuildLabels[fileA] = 1 << 0
	hfs.fileBuildLabels[fileB] = 1 << 1
	hfs.fileBuildLabels[fileC] = 1 << 2
	hfs.fileBuildLabels[fileShared] = (1 << 0) | (1 << 1)

	return hfs, fileA, fileB, fileC, fileShared
}

func checkFile(t *testing.T, path, name string, wantDeleted bool) {
	t.Helper()
	_, gotErr := os.Stat(path)
	if wantDeleted {
		if !os.IsNotExist(gotErr) {
			t.Errorf("os.Stat(%q)=_, %v; want os.ErrNotExist", name, gotErr)
		}
	} else {
		if gotErr != nil {
			t.Errorf("os.Stat(%q)=_, %v; want nil err", name, gotErr)
		}
	}
}

func checkLedger(t *testing.T, hfs *HashFS, path, name string, wantDeleted bool) {
	t.Helper()
	_, gotExists := hfs.fileBuildLabels[path]
	if wantDeleted {
		if gotExists {
			t.Errorf("hfs.fileBuildLabels[%q]=true; want false", name)
		}
	} else {
		if !gotExists {
			t.Errorf("hfs.fileBuildLabels[%q]=false; want true", name)
		}
	}
}

func TestGarbageCollectBuildLabels(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	tests := []struct {
		name                   string
		opts                   BuildLabelGCOptions
		wantErr                bool
		wantFilesDeleted       int
		wantBytesReclaimed     int64
		wantEvictedBuildLabels []string
		wantFileADeleted       bool
		wantFileBDeleted       bool
		wantFileCDeleted       bool
		wantFileSharedDeltd    bool
	}{
		{
			name: "Strategy_EvictLabels",
			opts: BuildLabelGCOptions{
				Strategy: BuildLabelGCSweepStrategy{EvictLabels: []string{"chromeos"}},
				DryRun:   false,
			},
			wantFilesDeleted:       1,
			wantBytesReclaimed:     3,
			wantEvictedBuildLabels: []string{"chromeos"},
			wantFileADeleted:       true,
			wantFileBDeleted:       false,
			wantFileCDeleted:       false,
			wantFileSharedDeltd:    false,
		},
		{
			name: "Strategy_KeepLabels_Regex",
			opts: BuildLabelGCOptions{
				Strategy: BuildLabelGCSweepStrategy{KeepLabels: []string{"and.*", "pix.*"}},
				DryRun:   false,
			},
			wantFilesDeleted:       1,
			wantBytesReclaimed:     3,
			wantEvictedBuildLabels: []string{"chromeos"},
			wantFileADeleted:       true,
			wantFileBDeleted:       false,
			wantFileCDeleted:       false,
			wantFileSharedDeltd:    false,
		},
		{
			name: "Strategy_RetainLastX",
			opts: BuildLabelGCOptions{
				Strategy: BuildLabelGCSweepStrategy{RetainLastX: 1},
				DryRun:   false,
			},
			wantFilesDeleted:       2,
			wantBytesReclaimed:     8,
			wantEvictedBuildLabels: []string{"chromeos", "pixel"},
			wantFileADeleted:       true,
			wantFileBDeleted:       false,
			wantFileCDeleted:       true,
			wantFileSharedDeltd:    false,
		},
		{
			name: "Strategy_OlderThan",
			opts: BuildLabelGCOptions{
				Strategy: BuildLabelGCSweepStrategy{OlderThan: 2 * time.Hour},
				DryRun:   false,
			},
			wantFilesDeleted:       1,
			wantBytesReclaimed:     5,
			wantEvictedBuildLabels: []string{"pixel"},
			wantFileADeleted:       false,
			wantFileBDeleted:       false,
			wantFileCDeleted:       true,
			wantFileSharedDeltd:    false,
		},
		{
			name: "DryRun",
			opts: BuildLabelGCOptions{
				Strategy: BuildLabelGCSweepStrategy{EvictLabels: []string{"pixel"}},
				DryRun:   true,
			},
			wantErr:                false,
			wantFilesDeleted:       1,
			wantBytesReclaimed:     5,
			wantEvictedBuildLabels: []string{"pixel"},
			wantFileADeleted:       false,
			wantFileBDeleted:       false,
			wantFileCDeleted:       false,
			wantFileSharedDeltd:    false,
		},
		{
			name: "NoStrategy_Error",
			opts: BuildLabelGCOptions{
				Strategy: BuildLabelGCSweepStrategy{},
				DryRun:   true,
			},
			wantErr: true,
		},
		{
			name: "MultipleStrategies_Error",
			opts: BuildLabelGCOptions{
				Strategy: BuildLabelGCSweepStrategy{
					RetainLastX: 1,
					OlderThan:   time.Hour,
				},
				DryRun: true,
			},
			wantErr: true,
		},
		{
			name: "InvalidRegex_Error",
			opts: BuildLabelGCOptions{
				Strategy: BuildLabelGCSweepStrategy{EvictLabels: []string{"["}},
				DryRun:   true,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hfs, fileA, fileB, fileC, fileShared := setupGCFileSystem(t)

			gotResult, gotErr := hfs.GarbageCollectBuildLabels(ctx, tt.opts)
			if tt.wantErr {
				if gotErr == nil {
					t.Fatalf("GarbageCollectBuildLabels(...)=_, nil; want error")
				}
				return
			}
			if gotErr != nil {
				t.Fatalf("GarbageCollectBuildLabels(...)=_, %v; want nil err", gotErr)
			}

			if gotResult.TotalFilesDeleted != tt.wantFilesDeleted {
				t.Errorf("TotalFilesDeleted=%d; want=%d", gotResult.TotalFilesDeleted, tt.wantFilesDeleted)
			}
			if gotResult.TotalBytesReclaimed != tt.wantBytesReclaimed {
				t.Errorf("TotalBytesReclaimed=%d; want=%d", gotResult.TotalBytesReclaimed, tt.wantBytesReclaimed)
			}

			sort.Strings(gotResult.EvictedBuildLabels)
			sort.Strings(tt.wantEvictedBuildLabels)
			if diff := cmp.Diff(tt.wantEvictedBuildLabels, gotResult.EvictedBuildLabels); diff != "" {
				t.Errorf("EvictedBuildLabels diff -want +got:\n%s", diff)
			}

			checkFile(t, fileA, "fileA", tt.wantFileADeleted)
			checkFile(t, fileB, "fileB", tt.wantFileBDeleted)
			checkFile(t, fileC, "fileC", tt.wantFileCDeleted)
			checkFile(t, fileShared, "fileShared", tt.wantFileSharedDeltd)

			checkLedger(t, hfs, fileA, "fileA", tt.wantFileADeleted && !tt.opts.DryRun)
			checkLedger(t, hfs, fileB, "fileB", tt.wantFileBDeleted && !tt.opts.DryRun)
			checkLedger(t, hfs, fileC, "fileC", tt.wantFileCDeleted && !tt.opts.DryRun)
		})
	}
}

func TestGarbageCollectBuildLabels_ContextCancel(t *testing.T) {
	t.Parallel()
	hfs, fileA, _, _, _ := setupGCFileSystem(t)

	// Pre-cancel the context to simulate an instant Ctrl-C SIGINT abort
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	opts := BuildLabelGCOptions{
		Strategy: BuildLabelGCSweepStrategy{EvictLabels: []string{"chromeos"}},
		DryRun:   false,
	}

	gotResult, gotErr := hfs.GarbageCollectBuildLabels(ctx, opts)
	if gotErr == nil {
		t.Fatalf("GarbageCollectBuildLabels with canceled context should return err")
	}

	if gotResult.TotalFilesDeleted != 0 {
		t.Errorf("TotalFilesDeleted=%d; want=0", gotResult.TotalFilesDeleted)
	}

	// Verify the recovery-path protected the target dictionary because no files were physically removed
	checkFile(t, fileA, "fileA", false)
	checkLedger(t, hfs, fileA, "fileA", false)
	if _, ok := hfs.buildLabelDictionary[0]; !ok {
		t.Errorf("Target ID 0 (chromeos) was erroneously stripped from dictionary during abort")
	}
}

func TestGarbageCollectBuildLabels_FailedDeletions(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("Windows does not securely process directory permission mocking for DeleteFile structurally.")
	}

	hfs, fileA, _, _, _ := setupGCFileSystem(t)

	dir := filepath.Dir(fileA)
	if err := os.Chmod(dir, 0555); err != nil {
		t.Skipf("Cannot mock OS-level directory lock dynamically on this architecture: %v", err)
	}
	defer os.Chmod(dir, 0755)

	opts := BuildLabelGCOptions{
		Strategy: BuildLabelGCSweepStrategy{EvictLabels: []string{"chromeos"}},
		DryRun:   false,
	}

	gotResult, gotErr := hfs.GarbageCollectBuildLabels(t.Context(), opts)
	if gotErr != nil {
		t.Fatalf("GarbageCollectBuildLabels should cleanly ignore physical IO limits: _, %v; want nil err", gotErr)
	}

	if gotResult.TotalFilesDeleted != 0 {
		t.Errorf("TotalFilesDeleted=%d; want 0", gotResult.TotalFilesDeleted)
	}
	if len(gotResult.FailedDeletions) != 1 {
		t.Errorf("len(FailedDeletions)=%d; want 1", len(gotResult.FailedDeletions))
	} else if filepath.Base(gotResult.FailedDeletions[0]) != "chromeos_exclusive" {
		t.Errorf("FailedDeletions[0]=%q; want target matching chromeos_exclusive", gotResult.FailedDeletions[0])
	}

	checkLedger(t, hfs, fileA, "fileA", false)
}
