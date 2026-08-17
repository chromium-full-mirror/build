// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package cdc

import (
	"errors"
	"fmt"
	"io"
	"iter"
	"math/bits"
)

// masks table for FastCDC 2020 bit patterns from destor / restic-FastCDC reference implementations.
var masks = [26]uint64{
	0,                  // 0: padding
	0,                  // 1: padding
	0,                  // 2: padding
	0,                  // 3: padding
	0,                  // 4: padding
	0x0000000001804110, // 5: unused except for NC 3
	0x0000000001803110, // 6: 64B
	0x0000000018035100, // 7: 128B
	0x0000001800035300, // 8: 256B
	0x0000019000353000, // 9: 512B
	0x0000590003530000, // 10: 1KB
	0x0000d90003530000, // 11: 2KB
	0x0000d90103530000, // 12: 4KB
	0x0000d90303530000, // 13: 8KB
	0x0000d90313530000, // 14: 16KB
	0x0000d90f03530000, // 15: 32KB
	0x0000d90303537000, // 16: 64KB
	0x0000d90703537000, // 17: 128KB
	0x0000d90707537000, // 18: 256KB
	0x0000d91707537000, // 19: 512KB
	0x0000d91747537000, // 20: 1MB
	0x0000d91767537000, // 21: 2MB
	0x0000d93767537000, // 22: 4MB
	0x0000d93777537000, // 23: 8MB
	0x0000d93777577000, // 24: 16MB
	0x0000db3777577000, // 25: unused except for NC 3
}

// FastCDCOptions configures the FastCDC 2020 algorithm.
type FastCDCOptions struct {
	MinSize       int
	AvgSize       int
	MaxSize       int
	Normalization int // 0 to 3, default 2
	Seed          uint32
}

// FastCDCOptionsFromAvgSize computes standard REv2 derived sizing:
// min_chunk_size = avg / 4, max_chunk_size = avg * 4, normalization = 2.
func FastCDCOptionsFromAvgSize(avgSizeBytes int, seed uint32) FastCDCOptions {
	return FastCDCOptions{
		MinSize:       avgSizeBytes / 4,
		AvgSize:       avgSizeBytes,
		MaxSize:       avgSizeBytes * 4,
		Normalization: 2,
		Seed:          seed,
	}
}

// DefaultFastCDCOptions returns standard defaults for REv2 (512 KiB average, normalization 2, seed 0).
func DefaultFastCDCOptions() FastCDCOptions {
	return FastCDCOptionsFromAvgSize(512*1024, 0)
}

// validate checks that the FastCDC options satisfy algorithm and REv2 requirements.
func (o FastCDCOptions) validate() error {
	if o.MinSize <= 0 {
		return fmt.Errorf("fastcdc minSize (%d) must be positive", o.MinSize)
	}
	if o.AvgSize < o.MinSize {
		return fmt.Errorf("fastcdc avgSize (%d) must be >= minSize (%d)", o.AvgSize, o.MinSize)
	}
	if o.MaxSize < o.AvgSize {
		return fmt.Errorf("fastcdc maxSize (%d) must be >= avgSize (%d)", o.MaxSize, o.AvgSize)
	}
	if (o.AvgSize & (o.AvgSize - 1)) != 0 {
		return fmt.Errorf("fastcdc avgSize (%d) must be a power of 2", o.AvgSize)
	}
	if o.Normalization < 0 || o.Normalization > 3 {
		return fmt.Errorf("fastcdc normalization (%d) must be between 0 and 3", o.Normalization)
	}
	bitsCount := bits.Len(uint(o.AvgSize)) - 1
	smallBits := bitsCount + o.Normalization
	largeBits := bitsCount - o.Normalization
	if smallBits > 25 || largeBits < 5 {
		return fmt.Errorf("fastcdc normalization level %d too extreme for avgSize %d", o.Normalization, o.AvgSize)
	}
	return nil
}

// FastCDC implements the canonical FastCDC 2020 algorithm satisfying the Chunker interface.
type FastCDC struct {
	opts        FastCDCOptions
	maskS       uint64
	maskL       uint64
	maskSLs     uint64
	maskLLs     uint64
	seed        uint64
	shiftedSeed uint64
}

// Ensure FastCDC implements Chunker.
var _ Chunker = (*FastCDC)(nil)

// NewFastCDC creates a FastCDC chunker with the provided configurable options.
func NewFastCDC(opts FastCDCOptions) (*FastCDC, error) {
	if opts.Normalization == 0 {
		opts.Normalization = 2
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}

	bitsCount := bits.Len(uint(opts.AvgSize)) - 1
	smallBits := bitsCount + opts.Normalization
	largeBits := bitsCount - opts.Normalization

	maskS := masks[smallBits]
	maskL := masks[largeBits]

	seed64 := uint64(opts.Seed)

	return &FastCDC{
		opts:        opts,
		maskS:       maskS,
		maskL:       maskL,
		maskSLs:     maskS << 1,
		maskLLs:     maskL << 1,
		seed:        seed64,
		shiftedSeed: seed64 << 1,
	}, nil
}

// Options returns the configured options.
func (f *FastCDC) Options() FastCDCOptions {
	return f.opts
}

// Chunks returns an iterator streaming chunks over reader.
func (f *FastCDC) Chunks(reader io.Reader) iter.Seq2[Chunk, error] {
	return func(yield func(Chunk, error) bool) {
		session := f.newSession(reader)
		for {
			chunk, err := session.nextChunk()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				yield(Chunk{}, err)
				return
			}
			if !yield(chunk, nil) {
				return
			}
		}
	}
}

// fastCDCSession manages streaming chunking state for FastCDC over an io.Reader.
type fastCDCSession struct {
	fastCdc       *FastCDC
	r             io.Reader
	buf           []byte
	bufStart      int
	bufEnd        int
	currentOffset int64
	eof           bool
}

// newSession starts a streaming FastCDC session over an io.Reader.
func (f *FastCDC) newSession(r io.Reader) *fastCDCSession {
	return &fastCDCSession{
		fastCdc: f,
		r:       r,
		buf:     make([]byte, f.opts.MaxSize*2),
	}
}

// nextChunk returns the next FastCDC chunk from the stream.
func (s *fastCDCSession) nextChunk() (Chunk, error) {
	available := s.bufEnd - s.bufStart
	if available < s.fastCdc.opts.MaxSize && !s.eof {
		if s.bufStart > 0 && available > 0 {
			copy(s.buf, s.buf[s.bufStart:s.bufEnd])
		}
		s.bufStart = 0
		s.bufEnd = available

		for s.bufEnd < len(s.buf) {
			n, err := s.r.Read(s.buf[s.bufEnd:])
			s.bufEnd += n
			if err != nil {
				if errors.Is(err, io.EOF) {
					s.eof = true
					break
				}
				return Chunk{}, fmt.Errorf("reading stream: %w", err)
			}
		}
		available = s.bufEnd - s.bufStart
	}

	if available == 0 {
		return Chunk{}, io.EOF
	}

	chunkLen := s.fastCdc.cut(s.buf, s.bufStart, available)

	chunkData := make([]byte, chunkLen)
	copy(chunkData, s.buf[s.bufStart:s.bufStart+chunkLen])

	chunk := Chunk{
		Offset: s.currentOffset,
		Data:   chunkData,
	}

	s.currentOffset += int64(chunkLen)
	s.bufStart += chunkLen

	return chunk, nil
}

func (f *FastCDC) cut(buf []byte, off, len int) int {
	if len <= f.opts.MinSize {
		return len
	}

	n := min(len, f.opts.MaxSize)
	center := min(n, f.opts.AvgSize)

	minLimit := f.opts.MinSize & ^1
	centerLimit := center & ^1
	remainingLimit := n & ^1

	s := f.seed
	sLs := f.shiftedSeed
	maskSLs := f.maskSLs
	maskS := f.maskS
	maskLLs := f.maskLLs
	maskL := f.maskL

	data := buf[off : off+n]
	var hash uint64

	// Below avgSize: use maskS
	for a := minLimit; a < centerLimit; a += 2 {
		hash = (hash << 2) + (gearLsTable[data[a]] ^ sLs)
		if (hash & maskSLs) == 0 {
			return a
		}
		hash = hash + (gearTable[data[a+1]] ^ s)
		if (hash & maskS) == 0 {
			return a + 1
		}
	}

	// Above avgSize: use maskL
	for a := centerLimit; a < remainingLimit; a += 2 {
		hash = (hash << 2) + (gearLsTable[data[a]] ^ sLs)
		if (hash & maskLLs) == 0 {
			return a
		}
		hash = hash + (gearTable[data[a+1]] ^ s)
		if (hash & maskL) == 0 {
			return a + 1
		}
	}

	return n
}
