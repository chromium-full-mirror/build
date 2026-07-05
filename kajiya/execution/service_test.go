// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execution

import (
	"testing"

	"github.com/google/uuid"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
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
