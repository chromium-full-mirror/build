// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package cdc provides Content-Defined Chunking (CDC) algorithms (FastCDC and RepMaxCDC) in Go.
package cdc

import (
	"io"
	"iter"
)

// Chunk represents a contiguous byte range produced by a CDC chunker.
// The Data slice is allocated on the heap and owned by the caller;
// the chunker iterator will not overwrite or reuse its backing array.
type Chunk struct {
	Offset int64
	Data   []byte
}

// Chunker defines a common streaming content-defined chunker interface.
// Both FastCDC and RepMaxCDC implement this interface.
type Chunker interface {
	// Chunks returns an iterator streaming chunks from an io.Reader.
	// Memory is strictly bounded to O(max_chunk_size) regardless of input file size.
	Chunks(r io.Reader) iter.Seq2[Chunk, error]
}
