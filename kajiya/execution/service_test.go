// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execution

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	errpb "google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
)

// mustFn returns the digest.Function for the given enum value, failing the
// test on unsupported values.
func mustFn(t testing.TB, v repb.DigestFunction_Value) digest.Function {
	t.Helper()
	fn, err := digest.Lookup(v)
	if err != nil {
		t.Fatal(err)
	}
	return fn
}

func TestDedupKey(t *testing.T) {
	const hash = "a9993e364706816aba3e25717850c26c9cd0d89d"
	d := digest.Digest{Hash: hash, SizeBytes: 3}
	sha1Fn := mustFn(t, repb.DigestFunction_SHA1)
	gitFn := mustFn(t, repb.DigestFunction_GITSHA1)
	op := uuid.New()

	// Same hash under different functions must not collide.
	if got, want := dedupKey(sha1Fn, d, op, false), "sha1/"+hash; got != want {
		t.Errorf("dedupKey(sha1) = %q, want %q", got, want)
	}
	if got, want := dedupKey(gitFn, d, op, false), "gitsha1/"+hash; got != want {
		t.Errorf("dedupKey(gitsha1) = %q, want %q", got, want)
	}
	if dedupKey(sha1Fn, d, op, false) == dedupKey(gitFn, d, op, false) {
		t.Errorf("dedupKey collides for sha1 and gitsha1 sharing hash %q", hash)
	}

	// DoNotCache uses the unique operation name so requests never merge.
	if got, want := dedupKey(sha1Fn, d, op, true), op.String(); got != want {
		t.Errorf("dedupKey(DoNotCache) = %q, want %q", got, want)
	}
}

// TestFormatMissingBlobsErrorSubjects verifies the REAPI missing-blob subject
// carries the digest-function segment for non-sha256 functions
// (blobs/<fn>/<hash>/<size>) and omits it for sha256 (blobs/<hash>/<size>), so
// clients retry uploads into the correct namespace.
func TestFormatMissingBlobsErrorSubjects(t *testing.T) {
	blake3Fn := mustFn(t, repb.DigestFunction_BLAKE3)
	blake3D := blake3Fn.FromBytes([]byte("blake3 content"))
	sha256D := digest.SHA256.FromBytes([]byte("sha256 content"))

	for _, tc := range []struct {
		name string
		fn   digest.Function
		d    digest.Digest
		want []string
	}{
		{
			name: "blake3",
			fn:   blake3Fn,
			d:    blake3D,
			want: []string{fmt.Sprintf("blobs/blake3/%s/%d", blake3D.Hash, blake3D.SizeBytes)},
		},
		{
			name: "sha256",
			fn:   digest.SHA256,
			d:    sha256D,
			want: []string{fmt.Sprintf("blobs/%s/%d", sha256D.Hash, sha256D.SizeBytes)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := formatMissingBlobsError(&blobstore.MissingBlobsError{
				Fn:    tc.fn,
				Blobs: []digest.Digest{tc.d},
			})
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("formatMissingBlobsError did not return a status: %v", err)
			}

			var got []string
			for _, d := range st.Details() {
				pf, ok := d.(*errpb.PreconditionFailure)
				if !ok {
					continue
				}
				for _, v := range pf.Violations {
					got = append(got, v.Subject)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("subjects = %v, want %v", got, tc.want)
			}
		})
	}
}
