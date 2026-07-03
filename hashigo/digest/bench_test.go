// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package digest

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"
)

// benchSizes spans the regimes where the digest functions differ: BLAKE3
// reaches its widest SIMD path only once it has many 1 KiB chunks to hash in
// parallel, SHA256TREE switches from plain SHA-256 to its interior Merkle tree
// above 1 KiB, VSO's page/block structure appears at 64 KiB pages and 2 MiB
// blocks, and GITSHA-1's per-blob header only matters when the blob is tiny.
var benchSizes = []struct {
	name string
	n    int
}{
	{"1KiB", 1 << 10},
	{"64KiB", 64 << 10},
	{"1MiB", 1 << 20},
	{"16MiB", 16 << 20},
	{"128MiB", 128 << 20},
}

// benchBuf returns n bytes of deterministic pseudo-random content. Content
// does not affect hashing speed; randomness just keeps the input honest.
func benchBuf(n int) []byte {
	b := make([]byte, n)
	r := rand.NewChaCha8([32]byte{})
	if _, err := r.Read(b); err != nil {
		panic(err)
	}
	return b
}

// BenchmarkFromBytes measures whole-buffer hashing throughput (the CAS Put
// path) for every supported digest function across blob sizes.
func BenchmarkFromBytes(b *testing.B) {
	for _, size := range benchSizes {
		buf := benchBuf(size.n)
		for _, fn := range Functions() {
			b.Run(fmt.Sprintf("%s/%s", size.name, fn), func(b *testing.B) {
				b.SetBytes(int64(size.n))
				for b.Loop() {
					fn.FromBytes(buf)
				}
			})
		}
	}
}

// BenchmarkFunctionString guards the zero-allocation contract of String(),
// which sits on per-digest hot paths (storage keys, resource names).
func BenchmarkFunctionString(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = SHA256.String()
	}
}

// BenchmarkFromReader measures streaming throughput (the FromFile path, which
// feeds the hasher through a pooled 32 KiB copy buffer).
func BenchmarkFromReader(b *testing.B) {
	for _, size := range benchSizes {
		buf := benchBuf(size.n)
		for _, fn := range Functions() {
			b.Run(fmt.Sprintf("%s/%s", size.name, fn), func(b *testing.B) {
				b.SetBytes(int64(size.n))
				for b.Loop() {
					if _, err := fn.FromReader(bytes.NewReader(buf), int64(size.n)); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
