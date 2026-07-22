// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"context"
	"math"
	"path/filepath"
	"time"

	pb "go.chromium.org/build/siso/hashfs/proto"
)

// UpdateBuildLabelMask applies the bitmask for the given build label to the provided files.
// It manages build label bit allocations dynamically up to 64 labels, utilizing an LRU
// eviction strategy if the mask space overflows.
func (hfs *HashFS) UpdateBuildLabelMask(ctx context.Context, execRoot, buildLabel string, files []string) {
	if buildLabel == "" || len(files) == 0 {
		return
	}

	hfs.ledgerMu.Lock()
	defer hfs.ledgerMu.Unlock()

	bitID := hfs.allocateBitIDLocked(buildLabel)
	mask := uint64(1) << bitID

	for _, f := range files {
		if !filepath.IsAbs(f) {
			f = filepath.Join(execRoot, f)
		}
		f = filepath.ToSlash(f)
		// Siso paths in .siso_fs_state are stored natively matching e.Name
		hfs.fileBuildLabels[f] |= mask
	}
}

// allocateBitIDLocked assigns a unique 0..63 bit ID for the buildLabel.
// It must be called with ledgerMu held.
func (hfs *HashFS) allocateBitIDLocked(buildLabel string) uint32 {
	var oldestID uint32
	var oldestTime int64 = math.MaxInt64

	// Check if build label already exists and find the oldest label for potential eviction.
	for id, metadata := range hfs.buildLabelDictionary {
		if metadata.BuildLabel == buildLabel {
			metadata.LastBuildTimestamp = time.Now().UnixNano()
			return id
		}
		if metadata.LastBuildTimestamp < oldestTime {
			oldestTime = metadata.LastBuildTimestamp
			oldestID = id
		}
	}

	// Try to allocate a new bit ID if there is room.
	if len(hfs.buildLabelDictionary) < 64 {
		var newID uint32 = 0
		for ; newID <= 63; newID++ {
			if _, ok := hfs.buildLabelDictionary[newID]; !ok {
				hfs.buildLabelDictionary[newID] = &pb.BuildLabelMetadata{
					BuildLabel:         buildLabel,
					LastBuildTimestamp: time.Now().UnixNano(),
				}
				return newID
			}
		}
	}

	// We are at capacity (64 items). Evict the oldest.
	evictMask := uint64(1) << oldestID
	clearMask := ^evictMask

	// Scrub the evicted bit from all files.
	for f, m := range hfs.fileBuildLabels {
		newMask := m & clearMask
		if newMask == 0 {
			delete(hfs.fileBuildLabels, f)
		} else {
			hfs.fileBuildLabels[f] = newMask
		}
	}

	// Re-purpose the oldest ID for the new buildLabel.
	hfs.buildLabelDictionary[oldestID] = &pb.BuildLabelMetadata{
		BuildLabel:         buildLabel,
		LastBuildTimestamp: time.Now().UnixNano(),
	}

	return oldestID
}
