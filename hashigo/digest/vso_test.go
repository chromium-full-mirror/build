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

// vsoIota returns n bytes where byte i is i&0xFF, matching the input pattern
// used by BuildXL's VsoHashTests (Enumerable.Range(0, n).Select(i => (byte)i)).
func vsoIota(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// TestVSOKnownVectors checks the streaming VSO hasher against the authoritative
// known-answer values from BuildXL's VsoHashTests.BlobIdsDoNotChange (the full
// 33-byte blob id, lowercased), across page- and block-size boundaries.
func TestVSOKnownVectors(t *testing.T) {
	fn, err := Lookup(rpb.DigestFunction_VSO)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		size int
		want string
	}{
		{0, "1e57cf2792a900d06c1cdfb3c453f35bc86f72788aa9724c96c929d1cc6b456a00"},
		{1, "3da32150b5e69b54e7ad1765d9573bc5e6e05d3b6529556c1b4a436a76a511f400"},
		{vsoPageSize - 1, "4ae1ad6462d75d117a5dafcf98167981371a4b21e1cee49d0b982de2ce01032300"},
		{vsoPageSize, "85840e1cb7cbfd78b464921c54c96f68c19066f20860efa8cce671b40ba5162300"},
		{vsoPageSize + 1, "d92a37c547f9d5b6b7b791a24f587da8189cca14ebc8511d2482e7448763e2bd00"},
		{vsoBlockSize - 1, "1c3c73f7e829e84a5ba05631195105fb49e033fa23bda6d379b3e46b5d73ef3700"},
		{vsoBlockSize, "6dae3ed3e623aed293297c289c3d20a53083529138b7631e99920ef0d93af3cd00"},
		{vsoBlockSize + 1, "1f9f3c008ea37ecb65bc5fb14a420cebb3ca72a9601ec056709a6b431f91807100"},
		{2*vsoBlockSize - 1, "df0e0db15e866592dbfa9bca74e6d547d67789f7eb088839fc1a5cefa862353700"},
		{2 * vsoBlockSize, "5e3a80b2acb2284cd21a08979c49cbb80874e1377940699b07a8abee9175113200"},
		{2*vsoBlockSize + 1, "b9a44a420593fa18453b3be7b63922df43c93ff52d88f2cab26fe1fadba7003100"},
	} {
		data := vsoIota(tc.size)

		// Whole-buffer path.
		if got := fn.FromBytes(data).Hash; got != tc.want {
			t.Errorf("FromBytes(size=%d) = %q, want %q", tc.size, got, tc.want)
		}
		// Streaming path (io.CopyBuffer with the package copy buffer).
		d, err := fn.FromReader(bytes.NewReader(data), int64(tc.size))
		if err != nil {
			t.Fatalf("FromReader(size=%d): %v", tc.size, err)
		}
		if d.Hash != tc.want {
			t.Errorf("FromReader(size=%d) = %q, want %q", tc.size, d.Hash, tc.want)
		}
		// Streaming with odd Write chunk boundaries that cross pages and blocks.
		h := newVSO()
		for off := 0; off < len(data); off += 7000 {
			h.Write(data[off:min(off+7000, len(data))])
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != tc.want {
			t.Errorf("chunked Write(size=%d) = %q, want %q", tc.size, got, tc.want)
		}
		// Sum must be idempotent (not mutate state).
		if got := hex.EncodeToString(h.Sum(nil)); got != tc.want {
			t.Errorf("second Sum(size=%d) = %q, want %q", tc.size, got, tc.want)
		}
	}
}

// vsoReference computes the VSO hash with a straightforward whole-buffer
// implementation of the BuildXL PagedHash spec, independent of the streaming
// vsoHasher. It is an oracle for additional, non-KAT sizes.
func vsoReference(data []byte) string {
	rolling := []byte("VSO Content Identifier Seed")
	nblocks := (len(data) + vsoBlockSize - 1) / vsoBlockSize
	if nblocks == 0 {
		nblocks = 1 // empty input is still one (empty) block.
	}
	for i := range nblocks {
		block := data[i*vsoBlockSize : min((i+1)*vsoBlockSize, len(data))]
		var pageHashes []byte
		for p := 0; p < len(block); p += vsoPageSize {
			ph := sha256.Sum256(block[p:min(p+vsoPageSize, len(block))])
			pageHashes = append(pageHashes, ph[:]...)
		}
		bh := sha256.Sum256(pageHashes)
		marker := byte(0x00)
		if i == nblocks-1 {
			marker = 0x01
		}
		buf := append(append(append([]byte{}, rolling...), bh[:]...), marker)
		s := sha256.Sum256(buf)
		rolling = s[:]
	}
	return hex.EncodeToString(rolling) + "00"
}

func TestVSOStreamingMatchesReference(t *testing.T) {
	for _, size := range []int{
		2*vsoPageSize - 1, 2 * vsoPageSize, 3 * vsoPageSize,
		vsoBlockSize + vsoPageSize, 2*vsoBlockSize + 12345, 3 * vsoBlockSize,
	} {
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i*31 + 7)
		}
		want := vsoReference(data)
		h := newVSO()
		for off := 0; off < len(data); off += 4096 {
			h.Write(data[off:min(off+4096, len(data))])
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			t.Errorf("chunked Write(size=%d) = %q, want %q", size, got, want)
		}
	}
}
