// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"fmt"

	errpb "google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

// Status returns e as a FailedPrecondition status with a PreconditionFailure
// that lists each blob as a MISSING violation. Each violation's subject is
// the blob's ByteStream resource name, blobs/[<function>/]<hash>/<size>, which
// omits the function for SHA-256 and the other functions that REAPI lets a
// server infer from the hash length.
func (e *MissingBlobsError) Status() *status.Status {
	// List each blob as a MISSING violation.
	violations := make([]*errpb.PreconditionFailure_Violation, 0, len(e.Blobs))
	seg := ""
	if !e.Fn.IsZero() {
		seg = e.Fn.ResourceNameSegment()
	}
	for _, b := range e.Blobs {
		// The function's segment tells the client which namespace to upload into.
		subject := fmt.Sprintf("blobs/%s/%d", b.Hash, b.SizeBytes)
		if seg != "" {
			subject = fmt.Sprintf("blobs/%s/%s/%d", seg, b.Hash, b.SizeBytes)
		}
		violations = append(violations, &errpb.PreconditionFailure_Violation{
			Type:    "MISSING",
			Subject: subject,
		})
	}

	// Wrap the violations in a FailedPrecondition status.
	st, err := status.New(codes.FailedPrecondition, "missing blobs").WithDetails(&errpb.PreconditionFailure{
		Violations: violations,
	})
	if err != nil {
		return status.Newf(codes.Internal, "failed to create status: %v", err)
	}
	return st
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
