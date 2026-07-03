// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package digest

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"math/bits"
)

// SHA256TREE is a binary Merkle tree over 1024-byte leaves, per the REAPI
// remote_execution.proto. Blobs of 1024 bytes or fewer hash as plain SHA-256.
// Larger blobs are split into a left part of length m = 2^k (the largest power
// of two strictly less than the length) and a right remainder; the two subtree
// hashes are combined by a single SHA-256 block-cipher invocation over
// Hash(left)||Hash(right) using the SHA-384 initial values and WITHOUT the
// Davies-Meyer feed-forward.
//
// The recursive split is the canonical left-perfect-subtree split, so the tree
// is reproduced by a BLAKE3-style chunk stack over 1024-byte leaves.
const sha256treeChunkSize = 1024

// sha384IV is the internal-node initial state: the eight SHA-384 initial hash
// values (fractional parts of the square roots of the 9th–16th primes), per
// remote_execution.proto. Using these instead of the SHA-256 IV prevents
// collisions between small and large blobs.
var sha384IV = [8]uint32{
	0xcbbb9d5d, 0x629a292a, 0x9159015a, 0x152fecd8,
	0x67332667, 0x8eb44a87, 0xdb0c2e0d, 0x47b5481d,
}

// sha256K are the SHA-256 round constants.
var sha256K = [64]uint32{
	0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
	0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
	0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
	0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
	0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
	0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
	0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
	0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
}

// sha256Compress runs the 64-round SHA-256 block function on the single 64-byte
// block m starting from state h, and returns the resulting state WITHOUT the
// Davies-Meyer feed-forward (the original h is not added back). This is the
// block-cipher primitive used to combine SHA256TREE subtree hashes.
func sha256Compress(h [8]uint32, m []byte) [8]uint32 {
	var w [64]uint32
	for i := range 16 {
		w[i] = binary.BigEndian.Uint32(m[i*4:])
	}
	for i := 16; i < 64; i++ {
		s0 := bits.RotateLeft32(w[i-15], -7) ^ bits.RotateLeft32(w[i-15], -18) ^ (w[i-15] >> 3)
		s1 := bits.RotateLeft32(w[i-2], -17) ^ bits.RotateLeft32(w[i-2], -19) ^ (w[i-2] >> 10)
		w[i] = w[i-16] + s0 + w[i-7] + s1
	}
	a, b, c, d, e, f, g, hh := h[0], h[1], h[2], h[3], h[4], h[5], h[6], h[7]
	for i := range 64 {
		s1 := bits.RotateLeft32(e, -6) ^ bits.RotateLeft32(e, -11) ^ bits.RotateLeft32(e, -25)
		ch := (e & f) ^ (^e & g)
		t1 := hh + s1 + ch + sha256K[i] + w[i]
		s0 := bits.RotateLeft32(a, -2) ^ bits.RotateLeft32(a, -13) ^ bits.RotateLeft32(a, -22)
		maj := (a & b) ^ (a & c) ^ (b & c)
		t2 := s0 + maj
		hh, g, f, e, d, c, b, a = g, f, e, d+t1, c, b, a, t1+t2
	}
	return [8]uint32{a, b, c, d, e, f, g, hh}
}

// sha256treeParent combines two subtree hashes into a parent node hash.
func sha256treeParent(left, right [32]byte) [32]byte {
	var m [64]byte
	copy(m[:32], left[:])
	copy(m[32:], right[:])
	state := sha256Compress(sha384IV, m[:])
	var out [32]byte
	for i, v := range state {
		binary.BigEndian.PutUint32(out[i*4:], v)
	}
	return out
}

// sha256treeHasher is a streaming hash.Hash implementing SHA256TREE. State is
// bounded to one partial 1024-byte chunk plus a stack of at most ~log2(N)
// subtree hashes.
//
// Leaves (1024-byte chunks) are combined with a BLAKE3-style stack: after
// committing chunk i (1-based count), while the count is even the new subtree
// is merged with the stack top. A full chunk is committed only once more data
// arrives, so the final (possibly short) chunk and the ≤1024-byte single-leaf
// case are handled by Sum.
type sha256treeHasher struct {
	buf   []byte     // current partial chunk, < sha256treeChunkSize unless deferred.
	stack [][32]byte // completed subtree hashes, bottom = largest.
	count uint64     // number of committed chunks.
}

func newSHA256Tree() *sha256treeHasher {
	h := &sha256treeHasher{}
	h.Reset()
	return h
}

func (h *sha256treeHasher) Reset() {
	h.buf = make([]byte, 0, sha256treeChunkSize)
	h.stack = h.stack[:0]
	h.count = 0
}

func (h *sha256treeHasher) Size() int      { return 32 }
func (h *sha256treeHasher) BlockSize() int { return sha256treeChunkSize }

// addChunk pushes a leaf/subtree hash and merges equal-size subtrees.
func (h *sha256treeHasher) addChunk(cv [32]byte) {
	h.count++
	c := h.count
	for c&1 == 0 {
		left := h.stack[len(h.stack)-1]
		h.stack = h.stack[:len(h.stack)-1]
		cv = sha256treeParent(left, cv)
		c >>= 1
	}
	h.stack = append(h.stack, cv)
}

func (h *sha256treeHasher) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		if len(h.buf) == sha256treeChunkSize {
			// Full chunk and more data follows, so it is not the last chunk.
			h.addChunk(sha256.Sum256(h.buf))
			h.buf = h.buf[:0]
		}
		take := min(sha256treeChunkSize-len(h.buf), len(p))
		h.buf = append(h.buf, p[:take]...)
		p = p[take:]
	}
	return n, nil
}

// Sum finalizes the tree. It operates on copies so the hasher state is
// unchanged and Sum may be called repeatedly.
func (h *sha256treeHasher) Sum(b []byte) []byte {
	if h.count == 0 {
		// The entire content is a single ≤1024-byte leaf (including empty):
		// plain SHA-256.
		leaf := sha256.Sum256(h.buf)
		return append(b, leaf[:]...)
	}
	// More than one chunk: the buffered bytes are the final leaf.
	stack := append([][32]byte(nil), h.stack...)
	count := h.count + 1
	cv := sha256.Sum256(h.buf)
	c := count
	for c&1 == 0 {
		left := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		cv = sha256treeParent(left, cv)
		c >>= 1
	}
	stack = append(stack, cv)
	// Fold the stack to the root, combining from the top (rightmost) down.
	root := stack[len(stack)-1]
	for i := len(stack) - 2; i >= 0; i-- {
		root = sha256treeParent(stack[i], root)
	}
	return append(b, root[:]...)
}

var _ hash.Hash = (*sha256treeHasher)(nil)
