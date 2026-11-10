// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjautil

import "testing"

func TestSlabNewAndUnnew(t *testing.T) {
	s := newSlab[int](2)
	s.unnew() // no-op when empty
	if len(s.allocs) != 0 || cap(s.allocs) != 2 {
		t.Fatalf("initial slab len=%d cap=%d; want len=0 cap=2", len(s.allocs), cap(s.allocs))
	}

	p0 := s.new()
	*p0 = 10
	p1 := s.new()
	*p1 = 20
	if p0 == p1 {
		t.Errorf("s.new() returned same pointer %p", p0)
	}

	s.unnew()
	p1Again := s.new()
	if p1Again != p1 {
		t.Errorf("s.new() after unnew = %p; want %p", p1Again, p1)
	}
	*p1Again = 21

	// Next new() overflows cap=2 and allocates a new backing slice.
	p2 := s.new()
	*p2 = 30
	if len(s.allocs) != 1 || cap(s.allocs) != 2 {
		t.Errorf("after rollover len=%d cap=%d; want len=1 cap=2", len(s.allocs), cap(s.allocs))
	}
	if *p0 != 10 || *p1Again != 21 || *p2 != 30 {
		t.Errorf("values = (%d, %d, %d); want (10, 21, 30)", *p0, *p1Again, *p2)
	}
}

func TestSlabSlice(t *testing.T) {
	s := newSlab[int](10)

	a := s.slice(3)
	if len(a) != 3 || cap(a) != 3 {
		t.Fatalf("slice(3): len=%d cap=%d; want len=3 cap=3", len(a), cap(a))
	}
	a[0], a[1], a[2] = 1, 2, 3

	b := s.slice(4)
	if len(b) != 4 || cap(b) != 4 {
		t.Fatalf("slice(4): len=%d cap=%d; want len=4 cap=4", len(b), cap(b))
	}
	b[0], b[1], b[2], b[3] = 4, 5, 6, 7

	// Appending to a must not overwrite b[0] because cap(a) == len(a).
	_ = append(a, 99)
	if b[0] != 4 {
		t.Errorf("append(a) overwrote b[0]=%d; want 4", b[0])
	}

	// Large request (n >= cap/2) allocates a standalone slice without advancing s.allocs.
	large := s.slice(5)
	if len(large) != 5 || cap(large) != 5 {
		t.Fatalf("slice(5): len=%d cap=%d; want len=5 cap=5", len(large), cap(large))
	}
	if len(s.allocs) != 7 {
		t.Errorf("after large slice(5), len(s.allocs)=%d; want 7", len(s.allocs))
	}

	// Tail overflow (7 + 4 > 10) allocates a fresh slab of cap=10 and serves from it,
	// so the following slice(4) shares the new slab instead of allocating again.
	c := s.slice(4)
	if len(s.allocs) != 4 || cap(s.allocs) != 10 {
		t.Fatalf("after rollover slice(4), len=%d cap=%d; want len=4 cap=10", len(s.allocs), cap(s.allocs))
	}
	d := s.slice(4)
	if len(s.allocs) != 8 {
		t.Fatalf("after second slice(4) in new slab, len=%d; want 8", len(s.allocs))
	}
	c[0] = 100
	d[0] = 200
	if s.allocs[0] != 100 || s.allocs[4] != 200 {
		t.Errorf("new slab backing values = (%d, %d); want (100, 200)", s.allocs[0], s.allocs[4])
	}
}
