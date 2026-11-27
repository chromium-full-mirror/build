// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjautil

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"runtime/trace"
	"sync"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/sync/errgroup"
)

// Magic numbers of zstd frames (RFC 8878, sections 3.1.1 and 3.1.2).
const (
	zstdFrameMagic     = 0xfd2fb528
	zstdSkippableMagic = 0x184d2a50
	zstdSkippableMask  = 0xfffffff0
)

// zstdMaxWindow is the largest window a manifest frame may use, the
// maximum of zstd --long.
const zstdMaxWindow = 1 << 31

// zstdMaxBlockSize is the most a compressed block can decompress to
// (RFC 8878, section 3.1.1.2.4).
const zstdMaxBlockSize = 128 << 10

// isZstd reports whether buf starts with a zstd frame. A Ninja manifest
// can't start with these bytes.
func isZstd(buf []byte) bool {
	if len(buf) < 4 {
		return false
	}
	magic := binary.LittleEndian.Uint32(buf)
	return magic == zstdFrameMagic || magic&zstdSkippableMask == zstdSkippableMagic
}

// zstdDecoder decodes whole frames with DecodeAll, which is safe for
// concurrent use.
var zstdDecoder = sync.OnceValues(func() (*zstd.Decoder, error) {
	return zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(runtime.GOMAXPROCS(0)),
		zstd.WithDecoderMaxWindow(zstdMaxWindow))
})

// zstdFrame is a data frame of a zstd stream.
type zstdFrame struct {
	src  []byte // the frame, from magic number to checksum
	size int    // decompressed size, or -1 if the frame doesn't declare it

	// decoded is set for frames of unknown size, which are decoded to out
	// before the others.
	decoded bool
	out     []byte
}

// decompressZstd decompresses the zstd frames in src. It decodes frames
// that declare their decompressed size concurrently, straight into the
// result.
func decompressZstd(ctx context.Context, src []byte) ([]byte, error) {
	defer trace.StartRegion(ctx, "ninja.decompress").End()
	frames, err := splitZstdFrames(src)
	if err != nil {
		return nil, err
	}
	dec, err := zstdDecoder()
	if err != nil {
		return nil, err
	}

	// Decode frames of unknown size first, to learn where the other
	// frames go.
	var eg errgroup.Group
	eg.SetLimit(runtime.GOMAXPROCS(0))
	for i := range frames {
		f := &frames[i]
		if f.size >= 0 {
			continue
		}
		f.decoded = true
		eg.Go(func() error {
			var err error
			f.out, err = dec.DecodeAll(f.src, nil)
			return err
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	if len(frames) == 1 && frames[0].decoded {
		return frames[0].out, nil
	}

	total := 0
	for i := range frames {
		f := &frames[i]
		if f.decoded {
			f.size = len(f.out)
		}
		if f.size > math.MaxInt-total {
			return nil, errors.New("zstd content size too large")
		}
		total += f.size
	}
	buf := make([]byte, total)
	off := 0
	for _, f := range frames {
		dst := buf[off : off : off+f.size]
		off += f.size
		if f.decoded {
			copy(dst[:f.size], f.out)
			continue
		}
		eg.Go(func() error {
			out, err := dec.DecodeAll(f.src, dst)
			if err != nil {
				return err
			}
			if len(out) != f.size {
				return fmt.Errorf("zstd frame decoded to %d bytes, header declares %d", len(out), f.size)
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return buf, nil
}

// splitZstdFrames splits src into its data frames, skipping skippable
// frames.
func splitZstdFrames(src []byte) ([]zstdFrame, error) {
	var frames []zstdFrame
	for off := 0; len(src) > 0; {
		var h zstd.Header
		if err := h.Decode(src); err != nil {
			return nil, fmt.Errorf("zstd frame header at offset %d: %w", off, err)
		}
		if h.Skippable {
			n := h.HeaderSize + int(h.SkippableSize)
			if n > len(src) {
				return nil, fmt.Errorf("zstd skippable frame: %w", io.ErrUnexpectedEOF)
			}
			src = src[n:]
			off += n
			continue
		}
		n, maxSize, err := zstdFrameLen(src, h.HeaderSize, h.HasCheckSum)
		if err != nil {
			return nil, err
		}
		size := -1
		if h.HasFCS {
			if h.FrameContentSize > uint64(maxSize) {
				return nil, fmt.Errorf("zstd frame at offset %d declares %d bytes, but its blocks hold at most %d", off, h.FrameContentSize, maxSize)
			}
			size = int(h.FrameContentSize)
		}
		frames = append(frames, zstdFrame{src: src[:n], size: size})
		src = src[n:]
		off += n
	}
	return frames, nil
}

// zstdFrameLen walks the block headers of the data frame at the start of
// src (RFC 8878, section 3.1.1.2). It returns the frame's length and the
// most bytes its blocks can decompress to.
func zstdFrameLen(src []byte, headerSize int, hasChecksum bool) (n, maxSize int, err error) {
	n = headerSize
	for {
		if n+3 > len(src) {
			return 0, 0, fmt.Errorf("zstd block header: %w", io.ErrUnexpectedEOF)
		}
		bh := uint32(src[n]) | uint32(src[n+1])<<8 | uint32(src[n+2])<<16
		n += 3
		last := bh&1 != 0
		size := int(bh >> 3)
		switch blockType := (bh >> 1) & 3; blockType {
		case 0: // raw
			n += size
			maxSize += size
		case 1: // RLE: one byte, repeated size times
			n++
			maxSize += size
		case 2: // compressed
			n += size
			maxSize += zstdMaxBlockSize
		default:
			return 0, 0, fmt.Errorf("zstd reserved block type %d", blockType)
		}
		if last {
			break
		}
	}
	if hasChecksum {
		n += 4
	}
	if n > len(src) {
		return 0, 0, fmt.Errorf("zstd frame: %w", io.ErrUnexpectedEOF)
	}
	return n, maxSize, nil
}
