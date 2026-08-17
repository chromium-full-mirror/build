// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package cdc

import (
	"errors"
	"fmt"
	"io"
	"iter"
	"slices"
)

// RepMaxCDCOptions configures the RepMaxCDC algorithm.
type RepMaxCDCOptions struct {
	MinSize     int
	HorizonSize int
}

// RepMaxCDCOptionsFromMinSize creates RepMaxCDCOptions with standard horizon default (8 * min).
func RepMaxCDCOptionsFromMinSize(minSizeBytes, horizonSizeBytes int) RepMaxCDCOptions {
	if horizonSizeBytes < 0 {
		horizonSizeBytes = 8 * minSizeBytes
	}
	return RepMaxCDCOptions{
		MinSize:     minSizeBytes,
		HorizonSize: horizonSizeBytes,
	}
}

// DefaultRepMaxCDCOptions returns standard defaults for REv2 (256 KiB min, 2 MiB horizon).
func DefaultRepMaxCDCOptions() RepMaxCDCOptions {
	return RepMaxCDCOptionsFromMinSize(256*1024, -1)
}

// validate checks that the RepMaxCDC options satisfy algorithm and REv2 requirements.
func (o RepMaxCDCOptions) validate() error {
	if o.MinSize < gearHashWindowSize {
		return fmt.Errorf("repmaxcdc minSize (%d) must be at least GearHashWindowSize (%d)", o.MinSize, gearHashWindowSize)
	}
	if o.HorizonSize < 0 {
		return fmt.Errorf("repmaxcdc horizonSize (%d) must be non-negative", o.HorizonSize)
	}
	return nil
}

// RepMaxCDC implements the canonical RepMaxCDC algorithm satisfying the Chunker interface.
type RepMaxCDC struct {
	opts     RepMaxCDCOptions
	peekSize int
}

// Ensure RepMaxCDC implements Chunker.
var _ Chunker = (*RepMaxCDC)(nil)

// NewRepMaxCDC creates a RepMaxCDC chunker with the provided configurable options.
func NewRepMaxCDC(opts RepMaxCDCOptions) (*RepMaxCDC, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	return &RepMaxCDC{
		opts:     opts,
		peekSize: 2*opts.MinSize + opts.HorizonSize,
	}, nil
}

// Options returns the configured options.
func (r *RepMaxCDC) Options() RepMaxCDCOptions {
	return r.opts
}

// Chunks returns an iterator streaming chunks over reader.
func (r *RepMaxCDC) Chunks(reader io.Reader) iter.Seq2[Chunk, error] {
	return func(yield func(Chunk, error) bool) {
		session := r.newSession(reader)
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

// repMaxSession manages streaming chunking state for a single io.Reader.
type repMaxSession struct {
	repmax            *RepMaxCDC
	r                 io.Reader
	buf               []byte
	bufStart          int
	bufEnd            int
	eof               bool
	previousChunkSize int
	completeChunks    []int
	incompleteChunks  []int
	currentHash       uint64
	bestHash          uint64
	currentOffset     int64
}

// newSession starts a streaming session over the given io.Reader.
func (r *RepMaxCDC) newSession(reader io.Reader) *repMaxSession {
	return &repMaxSession{
		repmax:           r,
		r:                reader,
		buf:              make([]byte, r.peekSize*2),
		completeChunks:   make([]int, 0, 64),
		incompleteChunks: make([]int, 0, 64),
	}
}

func (s *repMaxSession) peek(n int) (int, error) {
	available := s.bufEnd - s.bufStart
	if available < n && !s.eof {
		if s.bufStart > 0 && available > 0 {
			copy(s.buf, s.buf[s.bufStart:s.bufEnd])
		}
		s.bufStart = 0
		s.bufEnd = available

		for s.bufEnd < len(s.buf) {
			read, err := s.r.Read(s.buf[s.bufEnd:])
			s.bufEnd += read
			if err != nil {
				if errors.Is(err, io.EOF) {
					s.eof = true
					break
				}
				return 0, err
			}
		}
		available = s.bufEnd - s.bufStart
	}
	if n < available {
		return n, nil
	}
	return available, nil
}

// nextChunk reads data as needed from the underlying reader and returns the next chunk.
func (s *repMaxSession) nextChunk() (Chunk, error) {
	minSize := s.repmax.opts.MinSize

	s.bufStart += s.previousChunkSize
	s.previousChunkSize = 0

	if len(s.completeChunks) > 0 {
		firstChunk := s.completeChunks[len(s.completeChunks)-1]
		s.completeChunks = s.completeChunks[:len(s.completeChunks)-1]

		chunkData := make([]byte, firstChunk)
		copy(chunkData, s.buf[s.bufStart:s.bufStart+firstChunk])

		chunk := Chunk{
			Offset: s.currentOffset,
			Data:   chunkData,
		}

		s.previousChunkSize = firstChunk
		s.currentOffset += int64(firstChunk)
		return chunk, nil
	}

	dLen, err := s.peek(s.repmax.peekSize)
	if err != nil {
		return Chunk{}, fmt.Errorf("peeking stream: %w", err)
	}

	if dLen < 2*minSize {
		if dLen == 0 {
			return Chunk{}, io.EOF
		}
		chunkData := make([]byte, dLen)
		copy(chunkData, s.buf[s.bufStart:s.bufStart+dLen])

		chunk := Chunk{
			Offset: s.currentOffset,
			Data:   chunkData,
		}

		s.previousChunkSize = dLen
		s.currentOffset += int64(dLen)
		return chunk, nil
	}
	dLen -= minSize

	base := s.bufStart
	var currentChunk int
	var hash uint64
	var best uint64

	if len(s.incompleteChunks) >= 2 {
		currentChunk = s.incompleteChunks[len(s.incompleteChunks)-1]
		s.incompleteChunks = s.incompleteChunks[:len(s.incompleteChunks)-1]
		hash = s.currentHash
		best = s.bestHash
	} else {
		s.incompleteChunks = s.incompleteChunks[:0]
		s.incompleteChunks = append(s.incompleteChunks, 0)
		hash = 0
		for i := minSize - gearHashWindowSize; i < minSize; i++ {
			hash = (hash << 1) + gearTable[s.buf[base+i]]
		}
		best = hash
		currentChunk = 0
	}

	pos := minSize + currentChunk
	for {
		hashRegionLen := dLen - pos
		originalChunkCount := -1
		bytesBeforeMinChunkSize := s.incompleteChunks[len(s.incompleteChunks)-1] + minSize - 1 - currentChunk
		if hashRegionLen > bytesBeforeMinChunkSize {
			hashRegionLen = bytesBeforeMinChunkSize
			originalChunkCount = len(s.incompleteChunks)
		} else if hashRegionLen == 0 {
			break
		}

		p := base + pos
		data := s.buf[p : p+hashRegionLen]
		idx := 0
		for ; idx+4 <= hashRegionLen; idx += 4 {
			s1 := gearTable[data[idx]]
			s2 := (s1 << 1) + gearTable[data[idx+1]]
			s3 := (s2 << 1) + gearTable[data[idx+2]]
			s4 := (s3 << 1) + gearTable[data[idx+3]]
			h1 := (hash << 1) + s1
			h2 := (hash << 2) + s2
			h3 := (hash << 3) + s3
			h4 := (hash << 4) + s4
			hash = h4

			if h1 > best {
				best = h1
				s.incompleteChunks = append(s.incompleteChunks, currentChunk+idx+1)
			}
			if h2 > best {
				best = h2
				s.incompleteChunks = append(s.incompleteChunks, currentChunk+idx+2)
			}
			if h3 > best {
				best = h3
				s.incompleteChunks = append(s.incompleteChunks, currentChunk+idx+3)
			}
			if h4 > best {
				best = h4
				s.incompleteChunks = append(s.incompleteChunks, currentChunk+idx+4)
			}
		}
		for ; idx < hashRegionLen; idx++ {
			hash = (hash << 1) + gearTable[data[idx]]
			if hash > best {
				best = hash
				s.incompleteChunks = append(s.incompleteChunks, currentChunk+idx+1)
			}
		}

		if len(s.incompleteChunks) == originalChunkCount {
			prevCompleteCount := len(s.completeChunks)
			nextChunk := s.incompleteChunks[len(s.incompleteChunks)-1]
			for i := len(s.incompleteChunks) - 3; nextChunk >= minSize && i >= 0; i-- {
				chunk := s.incompleteChunks[i]
				if nextChunk-chunk >= minSize {
					s.completeChunks = append(s.completeChunks, nextChunk-chunk)
					nextChunk = chunk
					i--
				}
			}
			s.completeChunks = append(s.completeChunks, minSize+nextChunk)
			slices.Reverse(s.completeChunks[prevCompleteCount:])

			s.incompleteChunks = s.incompleteChunks[:1]
			currentChunk = 0
			hash = (hash << 1) + gearTable[s.buf[base+pos+hashRegionLen]]
			best = hash
			pos += hashRegionLen + 1
		} else {
			currentChunk += hashRegionLen
			pos += hashRegionLen
		}
	}

	s.incompleteChunks = append(s.incompleteChunks, currentChunk)
	var firstChunk int
	if len(s.completeChunks) > 0 {
		slices.Reverse(s.completeChunks)
		firstChunk = s.completeChunks[len(s.completeChunks)-1]
		s.completeChunks = s.completeChunks[:len(s.completeChunks)-1]
	} else {
		firstChunkIndex := len(s.incompleteChunks) - 2
		for maxChunk, i := s.incompleteChunks[firstChunkIndex]-minSize, firstChunkIndex-2; maxChunk >= 0 && i >= 0; i-- {
			chunk := s.incompleteChunks[i]
			if chunk <= maxChunk {
				firstChunkIndex = i
				maxChunk = chunk - minSize
				i--
			}
		}
		firstChunk = minSize + s.incompleteChunks[firstChunkIndex]

		reusableChunkIndex := firstChunkIndex + 1
		for {
			if reusableChunkIndex >= len(s.incompleteChunks) {
				s.incompleteChunks = s.incompleteChunks[:1]
				break
			}
			offsetInSecondChunk := s.incompleteChunks[reusableChunkIndex] - firstChunk
			if offsetInSecondChunk >= 0 {
				for i := reusableChunkIndex; i < len(s.incompleteChunks); i++ {
					s.incompleteChunks[i] -= firstChunk
				}

				if offsetInSecondChunk == 0 {
					copy(s.incompleteChunks, s.incompleteChunks[reusableChunkIndex:])
					s.incompleteChunks = s.incompleteChunks[:len(s.incompleteChunks)-reusableChunkIndex]
				} else {
					regionStart := base + firstChunk
					var recomputedHash uint64
					for i := minSize - gearHashWindowSize; i < minSize; i++ {
						recomputedHash = (recomputedHash << 1) + gearTable[s.buf[regionStart+i]]
					}
					s.incompleteChunks[0] = 0
					bestRecomputedHash := recomputedHash
					recomputedChunkIndex := 1
					originalChunksCount := len(s.incompleteChunks)

					for i := range offsetInSecondChunk - 1 {
						recomputedHash = (recomputedHash << 1) + gearTable[s.buf[regionStart+minSize+i]]
						if recomputedHash > bestRecomputedHash {
							bestRecomputedHash = recomputedHash
							recomputedChunk := i + 1
							if recomputedChunkIndex < reusableChunkIndex {
								s.incompleteChunks[recomputedChunkIndex] = recomputedChunk
								recomputedChunkIndex++
							} else {
								s.incompleteChunks = append(s.incompleteChunks, recomputedChunk)
							}
						}
					}

					if recomputedChunkIndex < reusableChunkIndex {
						copy(s.incompleteChunks[recomputedChunkIndex:], s.incompleteChunks[reusableChunkIndex:])
						s.incompleteChunks = s.incompleteChunks[:len(s.incompleteChunks)-(reusableChunkIndex-recomputedChunkIndex)]
					} else if len(s.incompleteChunks) > originalChunksCount {
						slices.Reverse(s.incompleteChunks[reusableChunkIndex:originalChunksCount])
						slices.Reverse(s.incompleteChunks[originalChunksCount:])
						slices.Reverse(s.incompleteChunks[reusableChunkIndex:])
					}
				}
				break
			}

			reusableChunkIndex++
			if reusableChunkIndex == len(s.incompleteChunks) {
				s.incompleteChunks = s.incompleteChunks[:1]
				break
			}
		}
	}

	chunkData := make([]byte, firstChunk)
	copy(chunkData, s.buf[s.bufStart:s.bufStart+firstChunk])

	chunk := Chunk{
		Offset: s.currentOffset,
		Data:   chunkData,
	}

	s.previousChunkSize = firstChunk
	s.currentHash = hash
	s.bestHash = best
	s.currentOffset += int64(firstChunk)

	return chunk, nil
}
