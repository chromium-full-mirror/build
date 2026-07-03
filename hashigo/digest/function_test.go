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
		// BLAKE3 vectors cross-checked against lukechampine.com/blake3 (an
		// implementation independent of the zeebo/blake3 dependency).
		{rpb.DigestFunction_BLAKE3, "", "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"},
		{rpb.DigestFunction_BLAKE3, "abc", "6437b3ac38465133ffb63b75273a8db548c558465d79db03fd359c6cd5bd9d85"},
		// GITSHA-1 vectors cross-checked with `git hash-object --stdin`.
		{rpb.DigestFunction_GITSHA1, "", "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"},
		{rpb.DigestFunction_GITSHA1, "abc", "f2ba8f84ab5c1bce84a7b441cb1959cfc7093b7f"},
		{rpb.DigestFunction_GITSHA1, "hello world", "95d09f2b10159347eece71399a7e2e907ea3df4f"},
		// MD5 (RFC 1321 test suite).
		{rpb.DigestFunction_MD5, "", "d41d8cd98f00b204e9800998ecf8427e"},
		{rpb.DigestFunction_MD5, "abc", "900150983cd24fb0d6963f7d28e17f72"},
		// SHA-384 / SHA-512 (FIPS 180-4 example vectors).
		{rpb.DigestFunction_SHA384, "", "38b060a751ac96384cd9327eb1b1e36a21fdb71114be07434c0cc7bf63f6e1da274edebfe76f65fbd51ad2f14898b95b"},
		{rpb.DigestFunction_SHA384, "abc", "cb00753f45a35e8bb5a03d699ac65007272c32ab0eded1631a8b605a43ff5bed8086072ba1e7cc2358baeca134c825a7"},
		{rpb.DigestFunction_SHA512, "", "cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e"},
		{rpb.DigestFunction_SHA512, "abc", "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f"},
		// MurmurHash3 x64_128 (seed 0), cross-checked against the C reference
		// via the twmb/murmur3 test corpus; hex is h1 (big-endian) || h2.
		{rpb.DigestFunction_MURMUR3, "", "00000000000000000000000000000000"},
		{rpb.DigestFunction_MURMUR3, "hello", "cbd8a7b341bd9b025b1e906a48ae1d19"},
		{rpb.DigestFunction_MURMUR3, "hello, world", "342fac623a5ebc8e4cdcbc079642414d"},
		{rpb.DigestFunction_MURMUR3, "The quick brown fox jumps over the lazy dog.", "cd99481f9ee902c9695da1a38987b6e7"},
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
		{"blake3", rpb.DigestFunction_BLAKE3, false},
		{"md5", rpb.DigestFunction_MD5, false},
		{"sha384", rpb.DigestFunction_SHA384, false},
		{"sha512", rpb.DigestFunction_SHA512, false},
		{"murmur3", rpb.DigestFunction_MURMUR3, false},
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
		{rpb.DigestFunction_MD5, ""},
		{rpb.DigestFunction_MURMUR3, ""},
		{rpb.DigestFunction_SHA384, ""},
		{rpb.DigestFunction_SHA512, ""},
		{rpb.DigestFunction_GITSHA1, "gitsha1"},
		{rpb.DigestFunction_BLAKE3, "blake3"},
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
	blake3Fn, err := Lookup(rpb.DigestFunction_BLAKE3)
	if err != nil {
		t.Fatal(err)
	}
	if !blake3Fn.Matches(rpb.DigestFunction_BLAKE3) {
		t.Errorf("blake3.Matches(BLAKE3) = false, want true")
	}
	if blake3Fn.Matches(rpb.DigestFunction_SHA256) {
		t.Errorf("blake3.Matches(SHA256) = true, want false")
	}
	if blake3Fn.Matches(rpb.DigestFunction_UNKNOWN) { // UNKNOWN canonicalizes to sha256.
		t.Errorf("blake3.Matches(UNKNOWN) = true, want false")
	}

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
		{32, rpb.DigestFunction_MD5}, // collision with MURMUR3 resolved to MD5.
		{96, rpb.DigestFunction_SHA384},
		{128, rpb.DigestFunction_SHA512},
		{7, rpb.DigestFunction_SHA256}, // no match falls back to SHA-256.
	} {
		if got := InferOmitted(tc.hexLen).Value(); got != tc.want {
			t.Errorf("InferOmitted(%d) = %v, want %v", tc.hexLen, got, tc.want)
		}
	}
}

func TestInferOmittedFrom(t *testing.T) {
	mustLookup := func(v rpb.DigestFunction_Value) Function {
		fn, err := Lookup(v)
		if err != nil {
			t.Fatalf("Lookup(%v): %v", v, err)
		}
		return fn
	}
	murmur3Fn := mustLookup(rpb.DigestFunction_MURMUR3)
	blake3Fn := mustLookup(rpb.DigestFunction_BLAKE3)
	advertised := []Function{SHA256, murmur3Fn, blake3Fn}
	for _, tc := range []struct {
		hexLen int
		want   rpb.DigestFunction_Value
		wantOK bool
	}{
		{32, rpb.DigestFunction_MURMUR3, true},  // resolves to MURMUR3, not global MD5 preference.
		{64, rpb.DigestFunction_SHA256, true},   // BLAKE3 is also 64 hex but never omits its segment.
		{40, rpb.DigestFunction_UNKNOWN, false}, // SHA1 not advertised.
	} {
		fn, ok := InferOmittedFrom(advertised, tc.hexLen)
		if ok != tc.wantOK {
			t.Errorf("InferOmittedFrom(advertised, %d) ok = %v, want %v", tc.hexLen, ok, tc.wantOK)
			continue
		}
		if ok && fn.Value() != tc.want {
			t.Errorf("InferOmittedFrom(advertised, %d) = %v, want %v", tc.hexLen, fn.Value(), tc.want)
		}
	}
}

func TestSupportedFunctions(t *testing.T) {
	fns := SupportedFunctions()
	if got, want := len(fns), 8; got != want {
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
