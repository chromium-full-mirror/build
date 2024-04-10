// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"fmt"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
)

// MissingBlobsError is an error type that indicates that one or more blobs are
// missing from the blob store. This is used to indicate that a client needs to
// upload the missing blobs before the operation can proceed.
type MissingBlobsError struct {
	// Blobs is the list of missing blobs.
	Blobs []digest.Digest
}

// Error implements the error interface for MissingBlobsError so that it can be
// used as an error value.
func (e *MissingBlobsError) Error() string {
	if len(e.Blobs) == 1 {
		return fmt.Sprintf("missing blob %s", e.Blobs[0])
	}
	return fmt.Sprintf("missing %d blobs", len(e.Blobs))
}
