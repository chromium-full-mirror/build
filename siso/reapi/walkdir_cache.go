// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package reapi

import (
	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// walkdirCacheMemLimitDivisor makes the default walkdirCache cap 1/20 of
// GOMEMLIMIT (see clockcache.ResolveBytes).
const walkdirCacheMemLimitDivisor = 20

// walkdirEntryBytes estimates the heap retained by caching dir.
func walkdirEntryBytes(d digest.Digest, dir *rpb.Directory) int64 {
	// rpb.Directory, digest key and cache entry.
	n := int64(320)
	if d.SizeBytes > 0 {
		// Names, symlink targets and hashes.
		n += d.SizeBytes
	}
	if dir != nil {
		// Go structs and pointer slices beyond the wire size.
		n += int64(len(dir.Files))*200 + int64(len(dir.Directories))*168 + int64(len(dir.Symlinks))*96
	}
	return n
}
