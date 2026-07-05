// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package server

import (
	"strings"
	"testing"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

func TestParseDigestFunctions(t *testing.T) {
	for _, tc := range []struct {
		names   string
		want    []repb.DigestFunction_Value
		wantErr string // substring of the expected error, "" for success.
	}{
		{names: "sha256", want: []repb.DigestFunction_Value{repb.DigestFunction_SHA256}},
		{names: "sha256, blake3", want: []repb.DigestFunction_Value{repb.DigestFunction_SHA256, repb.DigestFunction_BLAKE3}},
		{names: "murmur3", want: []repb.DigestFunction_Value{repb.DigestFunction_MURMUR3}},
		{names: "sha256,murmur3", want: []repb.DigestFunction_Value{repb.DigestFunction_SHA256, repb.DigestFunction_MURMUR3}},
		{names: "bogus", wantErr: "unknown digest function"},
		{names: "sha256,sha256", wantErr: "duplicate digest function"},
		// MD5 and MURMUR3 both omit the ByteStream digest-function segment
		// and have 32-char hashes, so an omitted segment would be ambiguous.
		{names: "md5,murmur3", wantErr: "only one of them can be enabled at a time"},
		{names: "sha256,murmur3,md5", wantErr: "only one of them can be enabled at a time"},
	} {
		fns, err := ParseDigestFunctions(tc.names)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ParseDigestFunctions(%q) err = %v, want containing %q", tc.names, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDigestFunctions(%q): %v", tc.names, err)
			continue
		}
		got := make([]repb.DigestFunction_Value, len(fns))
		for i, fn := range fns {
			got[i] = fn.Value()
		}
		if len(got) != len(tc.want) {
			t.Errorf("ParseDigestFunctions(%q) = %v, want %v", tc.names, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("ParseDigestFunctions(%q)[%d] = %v, want %v", tc.names, i, got[i], tc.want[i])
			}
		}
	}
}

func TestResolveDigestUnknownInfersFromAdvertised(t *testing.T) {
	murmur3Fn, err := digest.ParseFunction("murmur3")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{DigestFunctions: []digest.Function{digest.SHA256, murmur3Fn}}

	// A 32-hex digest with an UNKNOWN function resolves to murmur3 (the only
	// advertised 32-hex omitted-segment function), not the global MD5 default.
	d := murmur3Fn.FromBytes([]byte("hello"))
	fn, dg, err := cfg.ResolveDigest(repb.DigestFunction_UNKNOWN, d.Proto())
	if err != nil {
		t.Fatalf("ResolveDigest(UNKNOWN, murmur3 digest): %v", err)
	}
	if got, want := fn, murmur3Fn; got != want {
		t.Errorf("ResolveDigest fn = %v, want %v", got, want)
	}
	if got, want := dg, d; got != want {
		t.Errorf("ResolveDigest digest = %v, want %v", got, want)
	}

	// A 40-hex (SHA-1) digest matches no advertised function.
	sha1Fn, err := digest.ParseFunction("sha1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cfg.ResolveDigest(repb.DigestFunction_UNKNOWN, sha1Fn.FromBytes([]byte("hello")).Proto()); err == nil {
		t.Error("ResolveDigest(UNKNOWN, sha1 digest) = nil, want error")
	}

	// An explicit non-advertised function is rejected.
	if _, _, err := cfg.ResolveDigest(repb.DigestFunction_SHA1, sha1Fn.FromBytes([]byte("hello")).Proto()); err == nil {
		t.Error("ResolveDigest(SHA1, sha1 digest) = nil, want error")
	}
}
