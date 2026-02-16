// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package zstdstream

import (
	"io"
	"io/fs"
	"sync"

	"github.com/klauspost/compress/zstd"
)

var encoderPool = sync.Pool{
	New: func() any {
		d, err := zstd.NewWriter(nil)
		if err != nil {
			panic(err)
		}
		return d
	},
}

// CompressingReader is a Reader that reads data from a source, compresses it using Zstd and
// returns the compressed bytes to the caller.
type CompressingReader struct {
	pipeReader *io.PipeReader
}

// NewCompressingReader creates a CompressingReader.
func NewCompressingReader(src io.Reader) *CompressingReader {
	pipeReader, pipeWriter := io.Pipe()
	encoder := encoderPool.Get().(*zstd.Encoder)
	encoder.Reset(pipeWriter)
	go func() {
		_, err := encoder.ReadFrom(src)
		if closeErr := encoder.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		_ = pipeWriter.CloseWithError(err)
		encoder.Reset(nil)
		encoderPool.Put(encoder)
	}()

	return &CompressingReader{
		pipeReader: pipeReader,
	}
}

// Read reads as much data from the underlying source as necessary, compresses it with Zstd and
// fills p with compressed data, up to len(p) bytes. The number of compressed bytes put into p is
// returned as n (0 <= n <= len(p)). It returns any error encountered.
func (d *CompressingReader) Read(p []byte) (n int, err error) {
	if d == nil {
		return 0, fs.ErrInvalid
	}
	return d.pipeReader.Read(p)
}

// Close closes the underlying pipe, which will cause any pending reads to return an error.
func (d *CompressingReader) Close() error {
	if d == nil {
		return fs.ErrInvalid
	}
	return d.pipeReader.Close()
}
