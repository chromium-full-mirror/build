// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"math"
	"slices"
	"strings"
	"testing"
	"unsafe"
)

func symtabLen(s *symtab) int {
	n := 0
	s.m.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

func TestSymtabInternSlice(t *testing.T) {
	var s symtab
	long := strings.Repeat("x", 300)
	// Build the input from a larger string, so each element is a
	// substring of it.
	src := "clang -c " + long
	list := strings.Split(src, " ")

	got := s.InternSlice(list, 256)
	if !slices.Equal(got, list) {
		t.Fatalf("InternSlice(%q, 256)=%q; want equal", list, got)
	}
	// The table has copies, so that it doesn't keep src alive.
	for i := range 2 {
		if unsafe.StringData(got[i]) == unsafe.StringData(list[i]) {
			t.Errorf("InternSlice(...)[%d] shares memory with the input; want an interned copy", i)
		}
	}
	// The long string is neither interned nor copied.
	if unsafe.StringData(got[2]) != unsafe.StringData(list[2]) {
		t.Errorf("InternSlice(...)[2] is a copy of the input; want the input as is")
	}
	for _, v := range []string{"clang", "-c"} {
		if _, ok := s.Lookup(v); !ok {
			t.Errorf("Lookup(%q)=_, false; want interned", v)
		}
	}
	if _, ok := s.Lookup(long); ok {
		t.Errorf("Lookup(<%d bytes>)=_, true; want not interned", len(long))
	}

	// The same short strings are shared across slices.
	got2 := s.InternSlice([]string{"clang", "-c", long}, 256)
	if unsafe.StringData(got2[0]) != unsafe.StringData(got[0]) {
		t.Errorf("second InternSlice(...)[0] is not the interned string")
	}
	if unsafe.StringData(got2[2]) != unsafe.StringData(long) {
		t.Errorf("second InternSlice(...)[2] is a copy of the input; want the input as is")
	}
	if n := symtabLen(&s); n != 2 {
		t.Errorf("symtab has %d strings; want 2", n)
	}
}

func TestSymtabInternSlice_maxLen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		maxLen int
		v      string
		want   bool
	}{
		{name: "at_max_len", maxLen: 256, v: strings.Repeat("a", 256), want: true},
		{name: "over_max_len", maxLen: 256, v: strings.Repeat("a", 257), want: false},
		{name: "zero_max_len_empty", maxLen: 0, v: "", want: true},
		{name: "zero_max_len_nonempty", maxLen: 0, v: "a", want: false},
		{name: "max_int", maxLen: math.MaxInt, v: strings.Repeat("a", 1<<20), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s symtab
			got := s.InternSlice([]string{tc.v}, tc.maxLen)
			if len(got) != 1 {
				t.Fatalf("InternSlice([<%d bytes>], %d) returned %d strings; want 1", len(tc.v), tc.maxLen, len(got))
			}
			if got[0] != tc.v {
				t.Fatalf("InternSlice([<%d bytes>], %d)[0] has %d bytes that differ from the input", len(tc.v), tc.maxLen, len(got[0]))
			}
			if _, ok := s.Lookup(tc.v); ok != tc.want {
				t.Errorf("Lookup(<%d bytes>)=_, %t after InternSlice(_, %d); want %t", len(tc.v), ok, tc.maxLen, tc.want)
			}
			if !tc.want && unsafe.StringData(got[0]) != unsafe.StringData(tc.v) {
				t.Errorf("InternSlice([<%d bytes>], %d)[0] is a copy of the input; want the input as is", len(tc.v), tc.maxLen)
			}
		})
	}
}
