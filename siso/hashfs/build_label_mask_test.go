// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestHashFS_UpdateBuildLabelMask_LRU_Eviction(t *testing.T) {
	ctx := t.Context()

	// Create a new HashFS instance. We don't need a real working directory for this test.
	opt := Option{
		BuildLabel:    "test-label",
		StateFile:     filepath.Join(t.TempDir(), "test_hashfs.state"),
		CompressLevel: 0,
	}
	hfs, err := New(ctx, opt)
	if err != nil {
		t.Fatalf("Failed to create HashFS: %v", err)
	}
	defer hfs.Close(ctx)

	// Helper to directly peek at internal state for testing
	getBitID := func(label string) (uint32, bool) {
		hfs.ledgerMu.RLock()
		defer hfs.ledgerMu.RUnlock()
		for id, meta := range hfs.buildLabelDictionary {
			if meta.BuildLabel == label {
				return id, true
			}
		}
		return 0, false
	}

	getFileMask := func(fname string) uint64 {
		hfs.ledgerMu.RLock()
		defer hfs.ledgerMu.RUnlock()
		return hfs.fileBuildLabels[fname]
	}

	// 1. Fill the dictionary to its maximum capacity (64 items)
	for i := 1; i <= 64; i++ {
		labelName := fmt.Sprintf("label-%d", i)
		hfs.UpdateBuildLabelMask(ctx, "", labelName, []string{"shared_file.txt", fmt.Sprintf("file-%d.txt", i)})
		// Add a tiny sleep to ensure LastBuildTimestamp strictly orders the labels for LRU.
		time.Sleep(1 * time.Millisecond)
	}

	// Verify capacity
	hfs.ledgerMu.RLock()
	if len(hfs.buildLabelDictionary) != 64 {
		t.Fatalf("Got %d items in build label dictionary, want 64", len(hfs.buildLabelDictionary))
	}
	hfs.ledgerMu.RUnlock()

	// Verify "label-1" is in the dictionary (it should be the oldest)
	oldestID, ok := getBitID("label-1")
	if !ok {
		t.Fatalf("Expected label-1 to exist before eviction")
	}

	// Check that shared_file.txt has bits from all 64 labels
	mask := getFileMask("shared_file.txt")
	if mask != ^uint64(0) {
		t.Fatalf("Got shared_file.txt mask %x, want all 64 bits set", mask)
	}

	// 2. Add the 65th label, which should trigger eviction of "label-1"
	hfs.UpdateBuildLabelMask(ctx, "", "label-65", []string{"shared_file.txt", "file-65.txt"})

	// Verify "label-1" is evicted
	_, ok = getBitID("label-1")
	if ok {
		t.Errorf("Expected label-1 to be evicted (LRU)")
	}

	// Verify "label-65" got assigned the oldestID
	newID, ok := getBitID("label-65")
	if !ok {
		t.Fatalf("Expected label-65 to exist after eviction")
	}
	if newID != oldestID {
		t.Errorf("Got label-65 ID %d, want oldest ID %d", newID, oldestID)
	}

	// 3. Verify fileLabels bit scrubbing
	// The oldest bit should be scrubbed from "file-1.txt", dropping it from fileLabels entirely
	if getFileMask("file-1.txt") != 0 {
		t.Errorf("Expected file-1.txt to be removed from fileLabels since its only bit was evicted")
	}

	// "shared_file.txt" should no longer have the oldest bit from label-1, but wait!
	// label-64 was added to shared_file.txt and reused oldestID!
	// So shared_file.txt WILL have the newID bit set because it was re-added.
	// Let's check file-2.txt. It should still have its original bit.
	id2, _ := getBitID("label-2")
	if getFileMask("file-2.txt") != (1 << id2) {
		t.Errorf("Expected file-2.txt to retain its bitmask")
	}
}

func TestHashFS_UpdateBuildLabelMask_Basic(t *testing.T) {
	ctx := t.Context()
	hfs, err := New(ctx, Option{})
	if err != nil {
		t.Fatalf("Failed to create HashFS: %v", err)
	}
	defer hfs.Close(ctx)

	execRoot := "/root"
	if runtime.GOOS == "windows" {
		execRoot = "C:/root"
	}

	// 1. Try empty target and empty files
	hfs.UpdateBuildLabelMask(ctx, execRoot, "", []string{"file.txt"}) // Empty target
	hfs.UpdateBuildLabelMask(ctx, execRoot, "label-x", []string{})    // Empty files
	hfs.ledgerMu.RLock()
	if len(hfs.buildLabelDictionary) != 0 || len(hfs.fileBuildLabels) != 0 {
		t.Errorf("Got labels: %v, files: %v for empty inputs, want empty maps", hfs.buildLabelDictionary, hfs.fileBuildLabels)
	}
	hfs.ledgerMu.RUnlock()

	// 2. Test basic insertion and path normalization
	hfs.UpdateBuildLabelMask(ctx, execRoot, "label-a", []string{"file1.txt"})

	var idA uint32
	hfs.ledgerMu.RLock()
	for id, meta := range hfs.buildLabelDictionary {
		if meta.BuildLabel == "label-a" {
			idA = id
		}
	}
	hfs.ledgerMu.RUnlock()
	if idA == 0 && len(hfs.buildLabelDictionary) == 0 {
		t.Fatalf("label-a was not registered in dictionary")
	}

	// Check path key normalization
	expectedKey := "/root/file1.txt"
	if runtime.GOOS == "windows" {
		expectedKey = "C:/root/file1.txt"
	}

	hfs.ledgerMu.RLock()
	mask, exists := hfs.fileBuildLabels[expectedKey]
	hfs.ledgerMu.RUnlock()
	if !exists {
		t.Errorf("Expected key %q to exist in fileLabels", expectedKey)
	}
	if mask != (1 << idA) {
		t.Errorf("Got mask %b, want %b", mask, 1<<idA)
	}

	// 3. Test label reuse (updates timestamp, reuses same ID)
	hfs.UpdateBuildLabelMask(ctx, execRoot, "label-a", []string{"file2.txt"})
	hfs.ledgerMu.RLock()
	dictionaryLen := len(hfs.buildLabelDictionary)
	hfs.ledgerMu.RUnlock()
	if dictionaryLen != 1 {
		t.Errorf("Got %d labels in dictionary, want 1", dictionaryLen)
	}

	// 4. Test bitwise OR overlap on shared files
	hfs.UpdateBuildLabelMask(ctx, execRoot, "label-b", []string{"file2.txt"})

	var idB uint32
	hfs.ledgerMu.RLock()
	for id, meta := range hfs.buildLabelDictionary {
		if meta.BuildLabel == "label-b" {
			idB = id
		}
	}
	hfs.ledgerMu.RUnlock()

	expectedOverlapKey := "/root/file2.txt"
	if runtime.GOOS == "windows" {
		expectedOverlapKey = "C:/root/file2.txt"
	}

	hfs.ledgerMu.RLock()
	overlapMask := hfs.fileBuildLabels[expectedOverlapKey]
	hfs.ledgerMu.RUnlock()

	expectedMask := (uint64(1) << idA) | (uint64(1) << idB)
	if overlapMask != expectedMask {
		t.Errorf("Got overlap mask %b, want %b", overlapMask, expectedMask)
	}
}

func TestHashFS_UpdateBuildLabelMask_LRU_Ordering(t *testing.T) {
	ctx := t.Context()
	hfs, err := New(ctx, Option{})
	if err != nil {
		t.Fatalf("Failed to create HashFS: %v", err)
	}
	defer hfs.Close(ctx)

	checkLabelExists := func(label string) bool {
		hfs.ledgerMu.RLock()
		defer hfs.ledgerMu.RUnlock()
		for _, meta := range hfs.buildLabelDictionary {
			if meta.BuildLabel == label {
				return true
			}
		}
		return false
	}

	// 1. Fill the dictionary to capacity (64 labels)
	for i := 1; i <= 64; i++ {
		labelName := fmt.Sprintf("label-%d", i)
		hfs.UpdateBuildLabelMask(ctx, "", labelName, []string{"shared.txt"})
		time.Sleep(1 * time.Millisecond) // strictly order timestamps
	}

	// label-1 is now the oldest.
	// 2. Re-access label-1 to update its timestamp, making it the newest
	hfs.UpdateBuildLabelMask(ctx, "", "label-1", []string{"shared.txt"})
	time.Sleep(1 * time.Millisecond)

	// label-2 should now be the oldest
	// 3. Add label-65, which should evict label-2 (since label-1 was touched)
	hfs.UpdateBuildLabelMask(ctx, "", "label-65", []string{"shared.txt"})

	// label-1 should still exist!
	if !checkLabelExists("label-1") {
		t.Errorf("Expected label-1 to NOT be evicted because its timestamp was updated")
	}

	// label-2 should be evicted!
	if checkLabelExists("label-2") {
		t.Errorf("Expected label-2 to be evicted (LRU)")
	}
}
