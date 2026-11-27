// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjautil

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// zstdEncodeAll compresses data as one frame that declares its size.
func zstdEncodeAll(t *testing.T, data []byte, opts ...zstd.EOption) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil, opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll(data, nil)
}

// zstdEncodeStream compresses data as one frame without a declared size.
func zstdEncodeStream(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zstdRawFrame returns a frame holding data in one raw block, which
// declares contentSize as its size.
func zstdRawFrame(data []byte, contentSize uint64) []byte {
	frame := binary.LittleEndian.AppendUint32(nil, zstdFrameMagic)
	frame = append(frame, 0xe0) // single segment, 8-byte content size
	frame = binary.LittleEndian.AppendUint64(frame, contentSize)
	bh := uint32(len(data))<<3 | 1 // raw, last
	frame = append(frame, byte(bh), byte(bh>>8), byte(bh>>16))
	return append(frame, data...)
}

// zstdSkippable returns a skippable frame holding data.
func zstdSkippable(id byte, data []byte) []byte {
	frame := binary.LittleEndian.AppendUint32(nil, zstdSkippableMagic|uint32(id))
	frame = binary.LittleEndian.AppendUint32(frame, uint32(len(data)))
	return append(frame, data...)
}

func TestIsZstd(t *testing.T) {
	for _, tc := range []struct {
		name string
		buf  []byte
		want bool
	}{
		{name: "empty"},
		{name: "ninja", buf: []byte("rule cc\n  command = cc\n")},
		{name: "short", buf: []byte{0x28, 0xb5, 0x2f}},
		{name: "frame", buf: zstdEncodeAll(t, []byte("rule cc\n")), want: true},
		{name: "skippable", buf: zstdSkippable(0xf, nil), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isZstd(tc.buf); got != tc.want {
				t.Errorf("isZstd(%q)=%t; want=%t", tc.buf, got, tc.want)
			}
		})
	}
}

func TestDecompressZstd(t *testing.T) {
	var sb strings.Builder
	for i := range 20000 {
		fmt.Fprintf(&sb, "build obj/foo%d.o: cc ../../foo%d.cc\n", i, i)
	}
	part1 := []byte(sb.String())
	part2 := []byte("build all: phony obj/foo0.o\n")
	whole := append(append([]byte{}, part1...), part2...)

	for _, tc := range []struct {
		name string
		src  []byte
		want []byte
	}{
		{
			name: "one_frame",
			src:  zstdEncodeAll(t, whole),
			want: whole,
		},
		{
			name: "one_frame_with_checksum",
			src:  zstdEncodeAll(t, whole, zstd.WithEncoderCRC(true)),
			want: whole,
		},
		{
			name: "uncompressed_blocks",
			src:  zstdEncodeAll(t, whole, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithNoEntropyCompression(true)),
			want: whole,
		},
		{
			name: "frames_and_skippable_frames",
			src: bytes.Join([][]byte{
				zstdSkippable(0, []byte("index")),
				zstdEncodeAll(t, part1),
				zstdSkippable(3, nil),
				zstdEncodeAll(t, part2),
			}, nil),
			want: whole,
		},
		{
			name: "only_skippable_frame",
			src:  zstdSkippable(1, []byte("x")),
		},
		{
			name: "frames_with_and_without_size",
			src: bytes.Join([][]byte{
				zstdEncodeStream(t, part1[:1000]),
				zstdEncodeAll(t, part1[1000:]),
				zstdEncodeAll(t, nil),
				zstdEncodeAll(t, part2), // too small to declare its size
			}, nil),
			want: whole,
		},
		{
			name: "raw_frame",
			src:  zstdRawFrame(whole[:1000], 1000),
			want: whole[:1000],
		},
		{
			name: "one_frame_without_size",
			src:  zstdEncodeStream(t, whole),
			want: whole,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decompressZstd(t.Context(), tc.src)
			if err != nil {
				t.Fatalf("decompressZstd: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Errorf("decompressZstd: got %d bytes, want %d bytes", len(got), len(tc.want))
			}
		})
	}
}

func TestDecompressZstd_Errors(t *testing.T) {
	data := bytes.Repeat([]byte("build foo: phony bar\n"), 1000)
	frame := zstdEncodeAll(t, data, zstd.WithEncoderCRC(true))
	corrupt := bytes.Clone(frame)
	corrupt[len(corrupt)-1] ^= 0xff // checksum

	for _, tc := range []struct {
		name string
		src  []byte
	}{
		{name: "truncated_header", src: frame[:5]},
		{name: "truncated_block", src: frame[:len(frame)-10]},
		{name: "truncated_checksum", src: frame[:len(frame)-2]},
		{name: "truncated_skippable", src: zstdSkippable(0, []byte("abc"))[:9]},
		{name: "bad_checksum", src: corrupt},
		{name: "content_size_max", src: zstdRawFrame([]byte("x"), math.MaxInt64)},
		{name: "content_size_overflow", src: zstdRawFrame([]byte("x"), math.MaxUint64)},
		{name: "content_size_too_large", src: zstdRawFrame([]byte("x"), 2)},
		{name: "content_size_too_small", src: zstdRawFrame([]byte("xy"), 1)},
		{name: "trailing_garbage", src: append(bytes.Clone(frame), "build"...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decompressZstd(t.Context(), tc.src)
			if err == nil {
				t.Errorf("decompressZstd: got %d bytes; want error", len(got))
			}
		})
	}
}

func TestParser_Zstd(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"build.ninja": `
rule cc
  command = cc -c $in -o $out
rule link
  command = ld $in -o $out
subninja sub.ninja
build all: phony foo.o bar.o
`,
		"sub.ninja": `
build foo.o: cc foo.cc
build bar.o: cc bar.cc
build app: link foo.o bar.o
`,
	} {
		err := os.WriteFile(filepath.Join(dir, name), zstdEncodeAll(t, []byte(content)), 0644)
		if err != nil {
			t.Fatal(err)
		}
	}

	state := NewState()
	p := NewManifestParser(state)
	p.SetWd(dir)
	if err := p.Load(ctx, "build.ninja"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	node, ok := state.LookupNodeByPath("app")
	if !ok {
		t.Fatal("missing app")
	}
	edge, ok := node.InEdge()
	if !ok {
		t.Fatal("no inEdge for app")
	}
	if got, want := edge.Binding("command"), "ld foo.o bar.o -o app"; got != want {
		t.Errorf("app command=%q; want=%q", got, want)
	}
	if _, ok := state.LookupNodeByPath("foo.cc"); !ok {
		t.Error("missing foo.cc")
	}
}

func TestParser_ZstdError(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	frame := zstdEncodeAll(t, []byte("build all: phony\n"))
	err := os.WriteFile(filepath.Join(dir, "build.ninja"), frame[:len(frame)-1], 0644)
	if err != nil {
		t.Fatal(err)
	}

	state := NewState()
	p := NewManifestParser(state)
	p.SetWd(dir)
	err = p.Load(ctx, "build.ninja")
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Load: %v; want %v", err, io.ErrUnexpectedEOF)
	}
}
