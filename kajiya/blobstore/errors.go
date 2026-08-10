// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"fmt"

	"go.chromium.org/build/hashigo/digest"
)

// MissingBlobsError is an error type that indicates that one or more blobs are
// missing from the blob store. This is used to indicate that a client needs to
// upload the missing blobs before the operation can proceed.
type MissingBlobsError struct {
	// Fn is the digest function of the missing blobs.
	Fn digest.Function

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

// MissingSplitError is an error type that indicates that digest has
// no split information in the blob store.
type MissingSplitError struct {
	// Fn is the digest function of the missing blobs.
	Fn digest.Function

	// Blob is the digest to split.
	Blob digest.Digest
}

func (e *MissingSplitError) Error() string {
	return fmt.Sprintf("missing split for %s", e.Blob)
}

type UnexpectedSpliceError struct {
	// Blob is the digest to splice.
	Blob digest.Digest

	// TotalSize is total size of chunks of the splice.
	TotalSize int64
}

func (e *UnexpectedSpliceError) Error() string {
	return fmt.Sprintf("sum of chunk sizes (%d) does not match expected blob size (%d)", e.TotalSize, e.Blob.SizeBytes)
}
