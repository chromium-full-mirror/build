// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package digest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// sha256treeSeq returns n bytes of the repeating sequence 0,1,...,250,0,1,...
// (i mod 251), the input pattern used by the upstream SHA256TREE test vectors.
func sha256treeSeq(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// TestSHA256TreeKnownVectors checks the streaming hasher against the official
// sha256tree_test_vectors.txt from bazelbuild/remote-apis (referenced by
// remote_execution.proto). Inputs are the i-mod-251 sequence of the given
// length; lengths ≤1024 are plain SHA-256 and are checked separately below.
func TestSHA256TreeKnownVectors(t *testing.T) {
	fn, err := Lookup(rpb.DigestFunction_SHA256TREE)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		size int
		want string
	}{
		{1025, "36c0998b21839ef74300b9de47d96d1f62323dc81f2b4231e98ce70cd6ffe750"},
		{2048, "b584996386f01793751c5cf0c39561f51b7e9924b818943b3cb2f6928cea0fa9"},
		{2049, "7318d2029b0392edf4cf109edb5a086b4bdadbb7950f710a1483eb881d9e5d44"},
		{3072, "dfc61c0a041f79d55d53bfe31c6cda7df77fdc8e6fbac1143d70b7144fdf6937"},
		{3073, "517d20c0e5835f060a1bd6388ed68574f63424bdac2a2c3a35a5c2ef859d8fe2"},
		{4096, "2f72bb93880012168c027f6781527ff08177c7c8dccb443f4d2c6389c186633d"},
		{4097, "c3ec942c1b8f4580320d3a06bcf4f8fe1f5db2be797ab67061ea4c2a95f208f2"},
		{5120, "a76924f6535b4b473377c285ec27acc84cc58e95ab1e9e29b1bb6a4a3fb9d0b3"},
		{5121, "98f987c3e9fc057a70873715b679b89a663d0df806859b6ce73f8379b06a10ff"},
		{6144, "372f988af412041b680ab236feef45626380062beb7514bbf93607aedd28fc9a"},
		{6145, "6dc4b78efd770453417b2ffdc74b27054793efe6122ecd7ee098670ed7c4651c"},
		{7168, "43686312c0cabccf9d5ad509efa096e3d743c63c7a51f122473c57949e4dd9a0"},
		{7169, "ad729297ab36cd099665b27c4247474a5518e4cd0be443f5f31d95edda08429b"},
		{8192, "fcfdde6fe59178e17708c5ba647919c3b141a44c9d1970782e597e1465266932"},
		{8193, "113c6e3a2452f388b6fad13dfab66ee0bff597a0a9a517ad8d0165f7190b603e"},
		{16384, "a7a10149a8cb00be537000560edb83b196306b780b72fad8af218f369f75fc19"},
		{31744, "2cdf7662636c173d4b236f6ea03bf84c65e7f6487b53b2a61c420e26cf8a98c7"},
		{102400, "0668d69e5331840d2f1823d717b7b3f5d1fdc8a09504cddb692b87ff83d50e5f"},
	} {
		data := sha256treeSeq(tc.size)

		if got := fn.FromBytes(data).Hash; got != tc.want {
			t.Errorf("FromBytes(size=%d) = %q, want %q", tc.size, got, tc.want)
		}
		d, err := fn.FromReader(bytes.NewReader(data), int64(tc.size))
		if err != nil {
			t.Fatalf("FromReader(size=%d): %v", tc.size, err)
		}
		if d.Hash != tc.want {
			t.Errorf("FromReader(size=%d) = %q, want %q", tc.size, d.Hash, tc.want)
		}
		// Odd Write chunk boundaries that cross leaves.
		h := newSHA256Tree()
		for off := 0; off < len(data); off += 777 {
			h.Write(data[off:min(off+777, len(data))])
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != tc.want {
			t.Errorf("chunked Write(size=%d) = %q, want %q", tc.size, got, tc.want)
		}
		// Sum must be idempotent.
		if got := hex.EncodeToString(h.Sum(nil)); got != tc.want {
			t.Errorf("second Sum(size=%d) = %q, want %q", tc.size, got, tc.want)
		}
	}
}

// TestSHA256TreeSmallIsPlainSHA256 verifies that blobs of 1024 bytes or fewer
// hash identically to plain SHA-256, per the proto.
func TestSHA256TreeSmallIsPlainSHA256(t *testing.T) {
	fn, err := Lookup(rpb.DigestFunction_SHA256TREE)
	if err != nil {
		t.Fatal(err)
	}

	for _, size := range []int{0, 1, 100, 1023, 1024} {
		data := sha256treeSeq(size)
		want := hex.EncodeToString(func() []byte { s := sha256.Sum256(data); return s[:] }())
		if got := fn.FromBytes(data).Hash; got != want {
			t.Errorf("FromBytes(size=%d) = %q, want plain sha256 %q", size, got, want)
		}
	}
}
