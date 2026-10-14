// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"fmt"
	"slices"
	"testing"

	errpb "google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// TestMissingBlobsErrorStatus checks that Status reports each missing blob as
// a MISSING violation of a FailedPrecondition status. The subject includes the
// digest function's segment for BLAKE3 (blobs/blake3/<hash>/<size>) and omits
// it for SHA-256 (blobs/<hash>/<size>).
func TestMissingBlobsErrorStatus(t *testing.T) {
	// Make digests under two digest functions.
	blake3Fn := mustFn(t, repb.DigestFunction_BLAKE3)
	blake3D := blake3Fn.FromBytes([]byte("blake3 content"))
	sha256D := digest.SHA256.FromBytes([]byte("sha256 content"))
	otherD := digest.SHA256.FromBytes([]byte("other content"))

	for _, tc := range []struct {
		name  string
		fn    digest.Function
		blobs []digest.Digest
		want  []string
	}{
		{
			name:  "blake3",
			fn:    blake3Fn,
			blobs: []digest.Digest{blake3D},
			want:  []string{fmt.Sprintf("blobs/blake3/%s/%d", blake3D.Hash, blake3D.SizeBytes)},
		},
		{
			name:  "sha256",
			fn:    digest.SHA256,
			blobs: []digest.Digest{sha256D, otherD},
			want: []string{
				fmt.Sprintf("blobs/%s/%d", sha256D.Hash, sha256D.SizeBytes),
				fmt.Sprintf("blobs/%s/%d", otherD.Hash, otherD.SizeBytes),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Build the status of the missing blobs.
			st := (&MissingBlobsError{Fn: tc.fn, Blobs: tc.blobs}).Status()
			if got, want := st.Code(), codes.FailedPrecondition; got != want {
				t.Errorf("code = %v, want %v", got, want)
			}

			// Check its violations.
			var got []string
			for _, d := range st.Details() {
				pf, ok := d.(*errpb.PreconditionFailure)
				if !ok {
					continue
				}
				for _, v := range pf.Violations {
					if v.Type != "MISSING" {
						t.Errorf("violation %q has type %q, want MISSING", v.Subject, v.Type)
					}
					got = append(got, v.Subject)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("subjects = %v, want %v", got, tc.want)
			}
		})
	}
}
