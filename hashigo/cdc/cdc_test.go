// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package cdc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand"
	"testing"

	_ "embed"
)

//go:embed testdata/SekienAkashita.jpg
var testVectorImage []byte

func mustNewFastCDC(t *testing.T, opts FastCDCOptions) Chunker {
	t.Helper()
	c, err := NewFastCDC(opts)
	if err != nil {
		t.Fatalf("NewFastCDC failed: %v", err)
	}
	return c
}

func mustNewRepMaxCDC(t *testing.T, opts RepMaxCDCOptions) Chunker {
	t.Helper()
	c, err := NewRepMaxCDC(opts)
	if err != nil {
		t.Fatalf("NewRepMaxCDC failed: %v", err)
	}
	return c
}

type slowReader struct {
	data []byte
	pos  int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = s.data[s.pos]
	s.pos++
	return 1, nil
}

func generateRandomBytesLocal(size int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, size)
	r.Read(b)
	return b
}

func TestEmptyAndSmallInputs_FastCDC(t *testing.T) {
	chunker := mustNewFastCDC(t, DefaultFastCDCOptions())
	sizes := []int{0, 1, 10, 63, 64, 100, 1024}

	for _, size := range sizes {
		data := generateRandomBytesLocal(size, int64(size+1))
		reassembled := make([]byte, 0, size)
		var count int

		for chunk, err := range chunker.Chunks(bytes.NewReader(data)) {
			if err != nil {
				t.Fatalf("FastCDC failed on size %d: %v", size, err)
			}
			count++
			reassembled = append(reassembled, chunk.Data...)
		}

		if size == 0 {
			if count != 0 {
				t.Errorf("FastCDC on 0-byte input produced %d chunks, want 0", count)
			}
		} else {
			if count == 0 {
				t.Errorf("FastCDC on %d-byte input produced 0 chunks, want >= 1", size)
			}
		}

		if !bytes.Equal(reassembled, data) {
			t.Errorf("FastCDC reassembly mismatch on size %d", size)
		}
	}
}

func TestEmptyAndSmallInputs_RepMaxCDC(t *testing.T) {
	chunker := mustNewRepMaxCDC(t, DefaultRepMaxCDCOptions())
	sizes := []int{0, 1, 10, 63, 64, 100, 1024}

	for _, size := range sizes {
		data := generateRandomBytesLocal(size, int64(size+1))
		reassembled := make([]byte, 0, size)
		var count int

		for chunk, err := range chunker.Chunks(bytes.NewReader(data)) {
			if err != nil {
				t.Fatalf("RepMaxCDC failed on size %d: %v", size, err)
			}
			count++
			reassembled = append(reassembled, chunk.Data...)
		}

		if size == 0 {
			if count != 0 {
				t.Errorf("RepMaxCDC on 0-byte input produced %d chunks, want 0", count)
			}
		} else {
			if count == 0 {
				t.Errorf("RepMaxCDC on %d-byte input produced 0 chunks, want >= 1", size)
			}
		}

		if !bytes.Equal(reassembled, data) {
			t.Errorf("RepMaxCDC reassembly mismatch on size %d", size)
		}
	}
}

func TestSlowByteByByteReader_FastCDC(t *testing.T) {
	data := generateRandomBytesLocal(500*1024, 4567)
	fastCDC := mustNewFastCDC(t, FastCDCOptions{MinSize: 1024, AvgSize: 4096, MaxSize: 16384})
	sr := &slowReader{data: data}
	reassembled := make([]byte, 0, len(data))

	for chunk, err := range fastCDC.Chunks(sr) {
		if err != nil {
			t.Fatalf("FastCDC slow reader iteration error: %v", err)
		}
		reassembled = append(reassembled, chunk.Data...)
	}

	if !bytes.Equal(reassembled, data) {
		t.Fatalf("FastCDC slow reader reassembly mismatch")
	}
}

func TestSlowByteByByteReader_RepMaxCDC(t *testing.T) {
	data := generateRandomBytesLocal(500*1024, 4567)
	repMaxCDC := mustNewRepMaxCDC(t, RepMaxCDCOptions{MinSize: 1024, HorizonSize: 8192})
	sr := &slowReader{data: data}
	reassembled := make([]byte, 0, len(data))

	for chunk, err := range repMaxCDC.Chunks(sr) {
		if err != nil {
			t.Fatalf("RepMaxCDC slow reader iteration error: %v", err)
		}
		reassembled = append(reassembled, chunk.Data...)
	}

	if !bytes.Equal(reassembled, data) {
		t.Fatalf("RepMaxCDC slow reader reassembly mismatch")
	}
}

func TestDeterminism_FastCDC(t *testing.T) {
	data := generateRandomBytesLocal(2*1024*1024, 999)
	fastCDC := mustNewFastCDC(t, DefaultFastCDCOptions())

	var offsets1, lengths1 []int64
	for chunk, err := range fastCDC.Chunks(bytes.NewReader(data)) {
		if err != nil {
			t.Fatalf("FastCDC first run error: %v", err)
		}
		offsets1 = append(offsets1, chunk.Offset)
		lengths1 = append(lengths1, int64(len(chunk.Data)))
	}

	var idx int
	for chunk, err := range fastCDC.Chunks(bytes.NewReader(data)) {
		if err != nil {
			t.Fatalf("FastCDC second run error: %v", err)
		}
		if idx >= len(offsets1) {
			t.Fatalf("FastCDC second run produced extra chunk at index %d", idx)
		}
		if chunk.Offset != offsets1[idx] || int64(len(chunk.Data)) != lengths1[idx] {
			t.Errorf("FastCDC chunk %d mismatch on second run", idx)
		}
		idx++
	}

	if idx != len(offsets1) {
		t.Fatalf("FastCDC chunk count mismatch between runs: got %d, want %d", idx, len(offsets1))
	}
}

func TestDeterminism_RepMaxCDC(t *testing.T) {
	data := generateRandomBytesLocal(2*1024*1024, 999)
	repMaxCDC := mustNewRepMaxCDC(t, DefaultRepMaxCDCOptions())

	var offsets1, lengths1 []int64
	for chunk, err := range repMaxCDC.Chunks(bytes.NewReader(data)) {
		if err != nil {
			t.Fatalf("RepMaxCDC first run error: %v", err)
		}
		offsets1 = append(offsets1, chunk.Offset)
		lengths1 = append(lengths1, int64(len(chunk.Data)))
	}

	var idx int
	for chunk, err := range repMaxCDC.Chunks(bytes.NewReader(data)) {
		if err != nil {
			t.Fatalf("RepMaxCDC second run error: %v", err)
		}
		if idx >= len(offsets1) {
			t.Fatalf("RepMaxCDC second run produced extra chunk at index %d", idx)
		}
		if chunk.Offset != offsets1[idx] || int64(len(chunk.Data)) != lengths1[idx] {
			t.Errorf("RepMaxCDC chunk %d mismatch on second run", idx)
		}
		idx++
	}

	if idx != len(offsets1) {
		t.Fatalf("RepMaxCDC chunk count mismatch between runs: got %d, want %d", idx, len(offsets1))
	}
}

func TestBoundsAndReassembly_FastCDC(t *testing.T) {
	data := generateRandomBytesLocal(1*1024*1024, 42)
	opts := FastCDCOptions{
		MinSize: 512,
		AvgSize: 2048,
		MaxSize: 8192,
	}
	fastCDC := mustNewFastCDC(t, opts)

	reassembled := make([]byte, 0, len(data))
	var idx int
	for chunk, err := range fastCDC.Chunks(bytes.NewReader(data)) {
		if err != nil {
			t.Fatalf("unexpected streaming error: %v", err)
		}
		reassembled = append(reassembled, chunk.Data...)
		if len(chunk.Data) < opts.MinSize || len(chunk.Data) > opts.MaxSize {
			t.Errorf("chunk %d length %d out of bounds [%d, %d]", idx, len(chunk.Data), opts.MinSize, opts.MaxSize)
		}
		idx++
	}
	if idx == 0 {
		t.Fatalf("expected non-empty chunks")
	}
	if !bytes.Equal(reassembled, data) {
		t.Errorf("reassembled data does not match original data")
	}
}

func TestBoundsAndReassembly_RepMaxCDC(t *testing.T) {
	data := generateRandomBytesLocal(1*1024*1024, 42)
	minSize := 1024
	opts := RepMaxCDCOptions{
		MinSize:     minSize,
		HorizonSize: 8 * minSize,
	}
	repMaxCDC := mustNewRepMaxCDC(t, opts)

	reassembled := make([]byte, 0, len(data))
	var idx int
	for chunk, err := range repMaxCDC.Chunks(bytes.NewReader(data)) {
		if err != nil {
			t.Fatalf("unexpected streaming error: %v", err)
		}
		reassembled = append(reassembled, chunk.Data...)
		if len(chunk.Data) < minSize || len(chunk.Data) >= 2*minSize {
			t.Errorf("chunk %d length %d not in range [%d, %d)", idx, len(chunk.Data), minSize, 2*minSize)
		}
		idx++
	}
	if idx == 0 {
		t.Fatalf("expected non-empty chunks")
	}
	if !bytes.Equal(reassembled, data) {
		t.Errorf("reassembled data does not match original data")
	}
}

func TestOptionValidation_FastCDC(t *testing.T) {
	invalidCases := []FastCDCOptions{
		{MinSize: 0, AvgSize: 1024, MaxSize: 4096},
		{MinSize: 128, AvgSize: 64, MaxSize: 4096},
		{MinSize: 128, AvgSize: 1024, MaxSize: 512},
		{MinSize: 128, AvgSize: 1000, MaxSize: 4096},
		{MinSize: 128, AvgSize: 1024, MaxSize: 4096, Normalization: 4},
	}
	for i, opt := range invalidCases {
		if err := opt.validate(); err == nil {
			t.Errorf("FastCDC invalid option case %d expected error, got nil", i)
		}
	}
}

func TestOptionValidation_RepMaxCDC(t *testing.T) {
	invalidCases := []RepMaxCDCOptions{
		{MinSize: 32, HorizonSize: 1024},
		{MinSize: 1024, HorizonSize: -2},
	}
	for i, opt := range invalidCases {
		if err := opt.validate(); err == nil {
			t.Errorf("RepMaxCDC invalid option case %d expected error, got nil", i)
		}
	}
}

// TestOfficialTestVectors_FastCDC_Seed0 tests FastCDC test vectors matching Bazel's FastCdcChunkerTest:
// https://github.com/bazelbuild/bazel/blob/master/src/test/java/com/google/devtools/build/lib/remote/chunking/FastCdcChunkerTest.java
// and the Remote Execution API specification:
// https://github.com/bazelbuild/remote-apis/blob/master/build/bazel/remote/execution/v2/fastcdc2020_test_vectors.txt
func TestOfficialTestVectors_FastCDC_Seed0(t *testing.T) {
	data := testVectorImage

	fastCDC := mustNewFastCDC(t, FastCDCOptions{
		MinSize:       4096,
		AvgSize:       16384,
		MaxSize:       65535,
		Normalization: 2,
		Seed:          0,
	})

	expectedChunks := []struct {
		offset int64
		length int
		sha256 string
	}{
		{0, 19186, "0f9efa589121d5d9e9e2c4ace91337d77cae866537143f6f15a0ffd525a77c2d"},
		{19186, 19279, "c7c86a165573c16448cda35c9169742e85645af42be22889f8b96b8ee0ec7cb0"},
		{38465, 17354, "bc88521e28a8b4479cdea5f75aa721a24f3a0a7d0be903aa6d505c574e51e89d"},
		{55819, 16387, "4b8dac2652e4685c629d2bb1ae9d4448e676b86f2e67ca0b2fff3d9580184b79"},
		{72206, 19940, "c0a7062da6f2386c28e086ee0cedd5732252741269838773cff1ddb05b2df6ed"},
		{92146, 17320, "7fa5b12134dc75cd2ac8dc60d3a8f3c8d22f0ee9d4cf74a4aa937e2a0d2d79a5"},
	}

	var idx int
	for chunk, err := range fastCDC.Chunks(bytes.NewReader(data)) {
		if err != nil {
			t.Fatalf("unexpected streaming error at index %d: %v", idx, err)
		}
		if idx >= len(expectedChunks) {
			t.Fatalf("produced more chunks than expected %d", len(expectedChunks))
		}

		want := expectedChunks[idx]
		if chunk.Offset != want.offset || len(chunk.Data) != want.length {
			t.Errorf("chunk %d mismatch: got {offset: %d, length: %d}, want {offset: %d, length: %d}",
				idx, chunk.Offset, len(chunk.Data), want.offset, want.length)
		}

		hash := sha256.Sum256(chunk.Data)
		hashHex := hex.EncodeToString(hash[:])
		if hashHex != want.sha256 {
			t.Errorf("chunk %d SHA256 mismatch: got %s, want %s", idx, hashHex, want.sha256)
		}
		idx++
	}

	if idx != len(expectedChunks) {
		t.Fatalf("chunk count mismatch: got %d, want %d", idx, len(expectedChunks))
	}
}

// TestOfficialTestVectors_FastCDC_Seed666 tests FastCDC seed=666 test vectors matching Bazel's FastCdcChunkerTest:
// https://github.com/bazelbuild/bazel/blob/master/src/test/java/com/google/devtools/build/lib/remote/chunking/FastCdcChunkerTest.java
func TestOfficialTestVectors_FastCDC_Seed666(t *testing.T) {
	data := testVectorImage

	fastCDC := mustNewFastCDC(t, FastCDCOptions{
		MinSize:       4096,
		AvgSize:       16384,
		MaxSize:       65535,
		Normalization: 2,
		Seed:          666,
	})

	expectedChunks := []struct {
		offset int64
		length int
		sha256 string
	}{
		{0, 17635, "cb3a9d80a3569772d4ed331ca37ab0c862c759897b890fc1aac90a4f2ea3a407"},
		{17635, 17334, "d758c6b7b0b7eef1e996f8ccd17de6c645360b03a26c35541e7581348ac08944"},
		{34969, 19136, "24846aefd89e510594bae3e9d7d5ea5012067601512610fed126a3c57ba993f5"},
		{54105, 17467, "efa785e1fefb49f190e665f72fd246c1442079874508c312196da1fb3040d00b"},
		{71572, 23593, "a2f557bdd8d40d8faada963ad5f91ec54b10ccee7c5ae72754a65137592dc607"},
		{95165, 14301, "e131100b4a7147ccad19dc63c4a2fac1f5d8b644e1373eeb6803825024234efc"},
	}

	var idx int
	for chunk, err := range fastCDC.Chunks(bytes.NewReader(data)) {
		if err != nil {
			t.Fatalf("unexpected streaming error at index %d: %v", idx, err)
		}
		if idx >= len(expectedChunks) {
			t.Fatalf("produced more chunks than expected %d", len(expectedChunks))
		}

		want := expectedChunks[idx]
		if chunk.Offset != want.offset || len(chunk.Data) != want.length {
			t.Errorf("chunk %d mismatch: got {offset: %d, length: %d}, want {offset: %d, length: %d}",
				idx, chunk.Offset, len(chunk.Data), want.offset, want.length)
		}

		hash := sha256.Sum256(chunk.Data)
		hashHex := hex.EncodeToString(hash[:])
		if hashHex != want.sha256 {
			t.Errorf("chunk %d SHA256 mismatch: got %s, want %s", idx, hashHex, want.sha256)
		}
		idx++
	}

	if idx != len(expectedChunks) {
		t.Fatalf("chunk count mismatch: got %d, want %d", idx, len(expectedChunks))
	}
}

// TestOfficialTestVectors_RepMaxCDC_DefaultHorizon tests RepMaxCDC test vectors matching Bazel's RepMaxCdcChunkerTest:
// https://github.com/bazelbuild/bazel/blob/master/src/test/java/com/google/devtools/build/lib/remote/chunking/RepMaxCdcChunkerTest.java
// and the Buildbarn Go reference implementation:
// https://github.com/buildbarn/go-cdc
func TestOfficialTestVectors_RepMaxCDC_DefaultHorizon(t *testing.T) {
	data := testVectorImage

	repMaxCDC := mustNewRepMaxCDC(t, RepMaxCDCOptions{
		MinSize:     4096,
		HorizonSize: 32768,
	})

	expectedChunks := []struct {
		offset int64
		length int
		sha256 string
	}{
		{0, 7026, "3c386537b8c3200dd2ab6623f6096a576d07844a4a0d5d2994d13e24887dae46"},
		{7026, 5198, "cc7fb08694b471e3ba660b700b347775d1e7a55db8f308c176e2eb6776b5222c"},
		{12224, 5310, "096a3e0e42f0a8b7a5a3a337e9b5b4c7fc59abcd6f69486fb68fe4c5c156300f"},
		{17534, 4271, "3f4a0b00f9a04959806c9ce9e530fa5d761930ef6c8477117b58eb93196342e8"},
		{21805, 5278, "d0c2f82664d8887bd2d289a75baea1d5688e6ee586f8767149bfef9c6df8fe63"},
		{27083, 5588, "558e6b58c659f7463c846a0d18c155a86520d1ca69102f102635c08e8b0897e2"},
		{32671, 5017, "c3c66478eb8e1632768e6e13dc31e492ac78befca38e9db85525b4b274c5b5d7"},
		{37688, 6761, "a5ce22ca3a02323451ddca6db5319c26b5fc1abc5621a033c98cd3de154cd29d"},
		{44449, 5541, "ec4752962eda083bf78b24cb087e07451ced85d8c0d719f32c474f7dae8fd764"},
		{49990, 7612, "3d9e8dc0a5f14dee7db5eabf99bb636352081423f122a84fdb160cecc2ad01bb"},
		{57602, 5501, "f8c747a292f7fd6433945356ee314b978b423ac4c3d248c8fdf36c0693eb6006"},
		{63103, 4695, "71249e1031dc237ab6393381a30a10ef90d9a93f532c135b11cc0df60a767b5f"},
		{67798, 4362, "95c42ec307be0ce1efde6de814c668f05a31887fde9501b478faca225cf9b8c7"},
		{72160, 6221, "8869f4871a0f3fb63775e0fb7f10e170a59b71a6fa0a91a18fa3cf5fff987eba"},
		{78381, 7190, "d2f91644457863f716071e76e9b1046408820aa95b36471d5e70ee5c91b611a2"},
		{85571, 5858, "3507024624db4ff48ee546e984d7c60461d6a526aba7e4aa33833b162785e79e"},
		{91429, 4382, "9b19e98bc5d9b6f486c7c7281fd61044b8e5333363048b20ce3dcb2ee0172980"},
		{95811, 8022, "e56dbfa2a82695fed3c565857dbc644af91c983b55c13fe20d9e03bb89e009e2"},
		{103833, 5633, "7d76fde9af9a911c4e580db9227e5109c5de28101f3f26637c527e80215aad38"},
	}

	var idx int
	for chunk, err := range repMaxCDC.Chunks(bytes.NewReader(data)) {
		if err != nil {
			t.Fatalf("unexpected streaming error at index %d: %v", idx, err)
		}
		if idx >= len(expectedChunks) {
			t.Fatalf("produced more chunks than expected %d", len(expectedChunks))
		}

		want := expectedChunks[idx]
		if chunk.Offset != want.offset || len(chunk.Data) != want.length {
			t.Errorf("chunk %d mismatch: got {offset: %d, length: %d}, want {offset: %d, length: %d}",
				idx, chunk.Offset, len(chunk.Data), want.offset, want.length)
		}

		hash := sha256.Sum256(chunk.Data)
		hashHex := hex.EncodeToString(hash[:])
		if hashHex != want.sha256 {
			t.Errorf("chunk %d SHA256 mismatch: got %s, want %s", idx, hashHex, want.sha256)
		}
		idx++
	}

	if idx != len(expectedChunks) {
		t.Fatalf("chunk count mismatch: got %d, want %d", idx, len(expectedChunks))
	}
}

// TestOfficialTestVectors_RepMaxCDC_ZeroHorizon tests RepMaxCDC zero-horizon test vectors matching Bazel's RepMaxCdcChunkerTest:
// https://github.com/bazelbuild/bazel/blob/master/src/test/java/com/google/devtools/build/lib/remote/chunking/RepMaxCdcChunkerTest.java
func TestOfficialTestVectors_RepMaxCDC_ZeroHorizon(t *testing.T) {
	data := testVectorImage

	repMaxCDC := mustNewRepMaxCDC(t, RepMaxCDCOptions{
		MinSize:     8192,
		HorizonSize: 0,
	})

	expectedChunks := []struct {
		offset int64
		length int
		sha256 string
	}{
		{0, 8192, "716c5e702f4bee5f18426da9e9eb77d22aa936486741768b86fff472bc18c363"},
		{8192, 8192, "33df8556634b77338abee984190d1aa21efb10411edeae3820fd61a93e489f58"},
		{16384, 8192, "a05018146657a5c999868a9e3f6e94da715c519f929005ca50404eb5a30caf1f"},
		{24576, 8192, "e124b2b4e8847def80e1b8da2fbfa5b483c3bcab6cf2d88e50d598d6c2d2e65c"},
		{32768, 8192, "7855d8367f097e33d0d3afbdba0e17749309a0dfb48f3d373bdf94b9b8f378e1"},
		{40960, 8192, "782a8f7b5367f177b94da16dc14026a3af3dcfc25b6b53f2060eafee0bc16922"},
		{49152, 8192, "3be28a5d9d67d5f8f9b30fbe742406cfefb46c139b48a47eeb98837fbb55128b"},
		{57344, 8192, "e6d7615aeefa30776bd0de14ad0e9ac5a738a767c63aa8c3310aa85c49d16d44"},
		{65536, 8192, "316e9ca063049a490d8f8431f109f2eaf0bf74739b4c3f4be9da46144733c327"},
		{73728, 8192, "2b4a043bd77df955a9fa30358597a90bca046474917e91d3715205f9d674a3bf"},
		{81920, 8192, "78abb22658ea5642440139f1bf86382d4bca2d33858fd8e15a170f1c1ac19651"},
		{90112, 8192, "076758af278b0fb9fd0313db8f6b796639c23241e2c8d831503e272e069d9aea"},
		{98304, 11162, "e776b8d90b880e10e4fdc4f99ba3b0bfbe471f26362007a1775d4cab46a539c7"},
	}

	var idx int
	for chunk, err := range repMaxCDC.Chunks(bytes.NewReader(data)) {
		if err != nil {
			t.Fatalf("unexpected streaming error at index %d: %v", idx, err)
		}
		if idx >= len(expectedChunks) {
			t.Fatalf("produced more chunks than expected %d", len(expectedChunks))
		}

		want := expectedChunks[idx]
		if chunk.Offset != want.offset || len(chunk.Data) != want.length {
			t.Errorf("chunk %d mismatch: got {offset: %d, length: %d}, want {offset: %d, length: %d}",
				idx, chunk.Offset, len(chunk.Data), want.offset, want.length)
		}

		hash := sha256.Sum256(chunk.Data)
		hashHex := hex.EncodeToString(hash[:])
		if hashHex != want.sha256 {
			t.Errorf("chunk %d SHA256 mismatch: got %s, want %s", idx, hashHex, want.sha256)
		}
		idx++
	}

	if idx != len(expectedChunks) {
		t.Fatalf("chunk count mismatch: got %d, want %d", idx, len(expectedChunks))
	}
}

func BenchmarkFastCDC_Iterator_8MB(b *testing.B) {
	fastCDC, err := NewFastCDC(DefaultFastCDCOptions())
	if err != nil {
		b.Fatal(err)
	}
	data := generateRandomBytesLocal(8*1024*1024, 1)

	b.SetBytes(int64(len(data)))
	b.ResetTimer()

	for range b.N {
		for _, err := range fastCDC.Chunks(bytes.NewReader(data)) {
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkRepMaxCDC_Iterator_8MB(b *testing.B) {
	repMaxCDC, err := NewRepMaxCDC(DefaultRepMaxCDCOptions())
	if err != nil {
		b.Fatal(err)
	}
	data := generateRandomBytesLocal(8*1024*1024, 1)

	b.SetBytes(int64(len(data)))
	b.ResetTimer()

	for range b.N {
		for _, err := range repMaxCDC.Chunks(bytes.NewReader(data)) {
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}
