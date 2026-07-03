// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package digest

import (
	"bytes"
	"slices"
	"testing"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// TestRegistryConsistency checks per-entry invariants of the registry table:
// the one-shot and streaming paths must agree on every input, and git-framing
// functions must not define a one-shot sum (sumBytes prefers sum, which would
// skip the "blob <size>\0" header).
func TestRegistryConsistency(t *testing.T) {
	t.Parallel()
	for _, h := range registry {
		if h.gitFraming && h.sum != nil {
			t.Errorf("%s: gitFraming requires sum == nil (a one-shot sum would skip the git header)", h.fn)
		}
		// Kajiya's on-disk layout tells a per-function root apart from a
		// {00..ff} shard directory by name length alone.
		if len(h.name) == 2 {
			t.Errorf("%s: two-character function name collides with shard directory names", h.fn)
		}
		fn := Function{h}
		for _, input := range []string{"", "The quick brown fox jumps over the lazy dog."} {
			oneShot := fn.FromBytes([]byte(input))
			streamed, err := fn.FromReader(bytes.NewReader([]byte(input)), int64(len(input)))
			if err != nil {
				t.Fatalf("%s: FromReader(%q): %v", h.fn, input, err)
			}
			if oneShot != streamed {
				t.Errorf("%s: FromBytes(%q) = %v disagrees with FromReader = %v", h.fn, input, oneShot, streamed)
			}
			if got, want := len(oneShot.Hash), h.hexLen; got != want {
				t.Errorf("%s: len(hash) = %d, want %d", h.fn, got, want)
			}
		}
	}
}

func TestKnownVectors(t *testing.T) {
	for _, tc := range []struct {
		fn    rpb.DigestFunction_Value
		input string
		want  string
	}{
		{rpb.DigestFunction_SHA256, "", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{rpb.DigestFunction_SHA256, "abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{rpb.DigestFunction_SHA1, "", "da39a3ee5e6b4b0d3255bfef95601890afd80709"},
		{rpb.DigestFunction_SHA1, "abc", "a9993e364706816aba3e25717850c26c9cd0d89d"},
		// GITSHA-1 vectors cross-checked with `git hash-object --stdin`.
		{rpb.DigestFunction_GITSHA1, "", "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"},
		{rpb.DigestFunction_GITSHA1, "abc", "f2ba8f84ab5c1bce84a7b441cb1959cfc7093b7f"},
		{rpb.DigestFunction_GITSHA1, "hello world", "95d09f2b10159347eece71399a7e2e907ea3df4f"},
	} {
		fn, err := Lookup(tc.fn)
		if err != nil {
			t.Fatalf("Lookup(%v): %v", tc.fn, err)
		}

		// FromBytes path.
		if got := fn.FromBytes([]byte(tc.input)).Hash; got != tc.want {
			t.Errorf("FromBytes(%q) hash with %v = %q, want %q", tc.input, tc.fn, got, tc.want)
		}
		// streaming path with known size.
		d, err := fn.FromReader(bytes.NewReader([]byte(tc.input)), int64(len(tc.input)))
		if err != nil {
			t.Fatalf("FromReader(%q): %v", tc.input, err)
		}
		if d.Hash != tc.want {
			t.Errorf("FromReader(%q) hash with %v = %q, want %q", tc.input, tc.fn, d.Hash, tc.want)
		}
		if d.SizeBytes != int64(len(tc.input)) {
			t.Errorf("FromReader(%q) size = %d, want %d", tc.input, d.SizeBytes, len(tc.input))
		}
	}
}

func TestParseFunction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		want    rpb.DigestFunction_Value
		wantErr bool
	}{
		{"sha256", rpb.DigestFunction_SHA256, false},
		{"SHA256", rpb.DigestFunction_SHA256, false},
		{"sha1", rpb.DigestFunction_SHA1, false},
		{"gitsha1", rpb.DigestFunction_GITSHA1, false},
		{"unknown", rpb.DigestFunction_UNKNOWN, true}, // an enum, but not a function.
		{"bogus", rpb.DigestFunction_UNKNOWN, true},   // not an enum.
	} {
		got, err := ParseFunction(tc.name)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseFunction(%q) err = %v, wantErr %v", tc.name, err, tc.wantErr)
			continue
		}
		if err == nil && got.Value() != tc.want {
			t.Errorf("ParseFunction(%q) = %v, want %v", tc.name, got.Value(), tc.want)
		}
	}
}

func TestFunctionByName(t *testing.T) {
	for _, tc := range []struct {
		name           string
		wantFn         rpb.DigestFunction_Value
		wantRecognized bool
		wantErr        bool
	}{
		{"sha256", rpb.DigestFunction_SHA256, true, false},
		{"gitsha1", rpb.DigestFunction_GITSHA1, true, false},
		{"blobs", rpb.DigestFunction_UNKNOWN, false, false},   // resource-name path component.
		{"unknown", rpb.DigestFunction_UNKNOWN, false, false}, // enum 0 is not a function name.
	} {
		fn, recognized, err := FunctionByName(tc.name)
		if recognized != tc.wantRecognized {
			t.Errorf("FunctionByName(%q) recognized = %v, want %v", tc.name, recognized, tc.wantRecognized)
		}
		if (err != nil) != tc.wantErr {
			t.Errorf("FunctionByName(%q) err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
		if err == nil && recognized && fn.Value() != tc.wantFn {
			t.Errorf("FunctionByName(%q) = %v, want %v", tc.name, fn.Value(), tc.wantFn)
		}
	}
}

func TestResourceNameSegment(t *testing.T) {
	for _, tc := range []struct {
		fn   rpb.DigestFunction_Value
		want string
	}{
		{rpb.DigestFunction_UNKNOWN, ""}, // canonicalized to SHA-256 by Lookup.
		{rpb.DigestFunction_SHA256, ""},
		{rpb.DigestFunction_SHA1, ""}, // omitted, inferred by length.
		{rpb.DigestFunction_GITSHA1, "gitsha1"},
	} {
		fn, err := Lookup(tc.fn)
		if err != nil {
			t.Fatalf("Lookup(%v): %v", tc.fn, err)
		}
		if got := fn.ResourceNameSegment(); got != tc.want {
			t.Errorf("Lookup(%v).ResourceNameSegment() = %q, want %q", tc.fn, got, tc.want)
		}
	}
}

func TestMatches(t *testing.T) {
	if !SHA256.Matches(rpb.DigestFunction_UNKNOWN) { // UNKNOWN canonicalizes to sha256.
		t.Errorf("SHA256.Matches(UNKNOWN) = false, want true")
	}
	if !SHA256.Matches(rpb.DigestFunction_SHA256) {
		t.Errorf("SHA256.Matches(SHA256) = false, want true")
	}
}

func TestEmpties(t *testing.T) {
	sha1Fn, err := Lookup(rpb.DigestFunction_SHA1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sha1Fn.Empty().Hash, "da39a3ee5e6b4b0d3255bfef95601890afd80709"; got != want {
		t.Errorf("sha1.Empty().Hash = %q, want %q", got, want)
	}
	if sha1Fn.Empty().SizeBytes != 0 {
		t.Errorf("sha1.Empty().SizeBytes = %d, want 0", sha1Fn.Empty().SizeBytes)
	}
	// EmptyTree is the digest of a marshaled empty Tree proto, not the empty
	// blob; just confirm it has the sha1 length (40 hex chars).
	if got := len(sha1Fn.EmptyTree().Hash); got != 40 {
		t.Errorf("len(sha1.EmptyTree().Hash) = %d, want 40", got)
	}
}

func TestLookupCanonicalizesUnknown(t *testing.T) {
	fn, err := Lookup(rpb.DigestFunction_UNKNOWN)
	if err != nil {
		t.Fatal(err)
	}
	if fn != SHA256 {
		t.Errorf("Lookup(UNKNOWN) = %v, want SHA256", fn)
	}
}

func TestInferOmitted(t *testing.T) {
	for _, tc := range []struct {
		hexLen int
		want   rpb.DigestFunction_Value
	}{
		{64, rpb.DigestFunction_SHA256},
		{40, rpb.DigestFunction_SHA1},
		{7, rpb.DigestFunction_SHA256}, // no match falls back to SHA-256.
	} {
		if got := InferOmitted(tc.hexLen).Value(); got != tc.want {
			t.Errorf("InferOmitted(%d) = %v, want %v", tc.hexLen, got, tc.want)
		}
	}
}

func TestSupportedFunctions(t *testing.T) {
	fns := SupportedFunctions()
	if got, want := len(fns), 3; got != want {
		t.Errorf("len(SupportedFunctions()) = %d, want %d", got, want)
	}
	if got, want := fns[0], rpb.DigestFunction_SHA256; got != want {
		t.Errorf("SupportedFunctions()[0] = %v, want %v", got, want)
	}
	if !slices.IsSorted(fns) {
		t.Errorf("SupportedFunctions() = %v, want sorted by enum value", fns)
	}
}

func TestValidate(t *testing.T) {
	const sha256Hash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if _, err := SHA256.Validate(sha256Hash, 0); err != nil {
		t.Errorf("Validate(%q, 0) = %v, want nil", sha256Hash, err)
	}
	if _, err := SHA256.Validate(sha256Hash, -1); err == nil {
		t.Errorf("Validate(%q, -1) = nil, want error", sha256Hash)
	}
	if _, err := SHA256.Validate("da39a3ee5e6b4b0d3255bfef95601890afd80709", 0); err == nil {
		t.Errorf("Validate(sha1-length hash) = nil, want error")
	}
	bad := "Z" + sha256Hash[1:]
	if _, err := SHA256.Validate(bad, 0); err == nil {
		t.Errorf("Validate(%q, 0) = nil, want error", bad)
	}
}
