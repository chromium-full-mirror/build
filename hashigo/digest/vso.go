// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package digest

import (
	"crypto/sha256"
	"hash"
)

// VSO-Hash (Microsoft BuildXL "PagedHash") constants. See
// https://github.com/microsoft/BuildXL/blob/main/Documentation/Specs/PagedHash.md
// and the reference implementation VsoHash.cs.
const (
	vsoPageSize      = 64 * 1024 // SHA-256 is computed per 64 KiB page.
	vsoPagesPerBlock = 32        // 32 pages per 2 MiB block.
	vsoBlockSize     = vsoPagesPerBlock * vsoPageSize
	vsoHashSize      = 32 + 1 // 32-byte rolling id + 1 algorithm-id byte.
	vsoAlgorithmID   = 0x00   // appended to the rolling id (AlgorithmId.File).
)

// vsoSeed is the fixed ASCII string that initializes the rolling blob id.
var vsoSeed = []byte("VSO Content Identifier Seed")

// vsoHasher is a streaming hash.Hash implementing VSO-Hash. State is bounded to
// one partial page (≤64 KiB) plus one block's page-hash concatenation (≤1 KiB).
//
// The blob is split into 2 MiB blocks; every block but the last is exactly
// 2 MiB. A block hash is SHA-256 over the concatenation of its pages' SHA-256
// hashes. The rolling id starts at vsoSeed and is updated per block as
// SHA-256(rollingId || blockHash || marker), where marker is 0x01 for the final
// block and 0x00 otherwise. The result is rollingId || 0x00.
//
// Because "final" is only known at Sum, a full (2 MiB) block is folded with the
// non-final marker only once more data arrives; the trailing in-progress block
// is folded as final by Sum.
type vsoHasher struct {
	rollingID  []byte // seed initially, then the 32-byte rolling SHA-256.
	page       []byte // current partial page, < vsoPageSize.
	pageHashes []byte // concatenated page hashes of the current block, ≤ 32*32 B.
	pageCount  int    // completed pages in the current block, 0..32.
}

func newVSO() *vsoHasher {
	h := &vsoHasher{}
	h.Reset()
	return h
}

func (h *vsoHasher) Reset() {
	h.rollingID = append([]byte(nil), vsoSeed...)
	h.page = make([]byte, 0, vsoPageSize)
	h.pageHashes = make([]byte, 0, vsoPagesPerBlock*sha256.Size)
	h.pageCount = 0
}

func (h *vsoHasher) Size() int      { return vsoHashSize }
func (h *vsoHasher) BlockSize() int { return vsoPageSize }

// completePage hashes the buffered full page into the current block.
func (h *vsoHasher) completePage() {
	h.hashPage(h.page)
	h.page = h.page[:0]
}

// hashPage appends the SHA-256 of one full page to pageHashes.
func (h *vsoHasher) hashPage(page []byte) {
	sum := sha256.Sum256(page)
	h.pageHashes = append(h.pageHashes, sum[:]...)
	h.pageCount++
}

// fold updates the rolling id with blockHash and the final/non-final marker.
func (h *vsoHasher) fold(blockHash []byte, isFinal bool) {
	marker := byte(0x00)
	if isFinal {
		marker = 0x01
	}
	buf := make([]byte, 0, len(h.rollingID)+len(blockHash)+1)
	buf = append(buf, h.rollingID...)
	buf = append(buf, blockHash...)
	buf = append(buf, marker)
	sum := sha256.Sum256(buf)
	h.rollingID = sum[:]
}

func (h *vsoHasher) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		if h.pageCount == vsoPagesPerBlock {
			// The current block is a full 2 MiB and more data follows, so it
			// is not the final block.
			bh := sha256.Sum256(h.pageHashes)
			h.fold(bh[:], false)
			h.pageHashes = h.pageHashes[:0]
			h.pageCount = 0
		}
		if len(h.page) == 0 && len(p) >= vsoPageSize {
			// Full page available: hash it from p in place instead of
			// staging it in h.page.
			h.hashPage(p[:vsoPageSize])
			p = p[vsoPageSize:]
			continue
		}
		take := min(vsoPageSize-len(h.page), len(p))
		h.page = append(h.page, p[:take]...)
		p = p[take:]
		if len(h.page) == vsoPageSize {
			h.completePage()
		}
	}
	return n, nil
}

// Sum folds the trailing in-progress block as the final block. It operates on
// copies so the hasher state is unchanged and Sum may be called repeatedly.
func (h *vsoHasher) Sum(b []byte) []byte {
	rollingID := append([]byte(nil), h.rollingID...)
	pageHashes := append([]byte(nil), h.pageHashes...)
	if len(h.page) > 0 {
		ps := sha256.Sum256(h.page)
		pageHashes = append(pageHashes, ps[:]...)
	}
	bh := sha256.Sum256(pageHashes)
	buf := make([]byte, 0, len(rollingID)+len(bh)+1)
	buf = append(buf, rollingID...)
	buf = append(buf, bh[:]...)
	buf = append(buf, 0x01)
	final := sha256.Sum256(buf)
	b = append(b, final[:]...)
	return append(b, vsoAlgorithmID)
}

var _ hash.Hash = (*vsoHasher)(nil)
