// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjautil

// slab is pre-batch allocated (without limits) memory to reduce allocations and impact to GC.
type slab[T any] struct {
	allocs []T // len for used, cap for allocated.
}

// newSlab creates new slab for n of T.
func newSlab[T any](n int) slab[T] {
	return slab[T]{
		allocs: make([]T, 0, n),
	}
}

// new returns *T from slab.
func (s *slab[T]) new() *T {
	if len(s.allocs) >= cap(s.allocs) {
		s.allocs = make([]T, 0, cap(s.allocs))
	}
	i := len(s.allocs)
	s.allocs = s.allocs[:i+1]
	return &s.allocs[i]
}

// unnew puts last *T returned by new back in slab.
func (s *slab[T]) unnew() {
	if len(s.allocs) > 0 {
		s.allocs = s.allocs[:len(s.allocs)-1]
	}
}

// slice returns slices [n]T.
func (s *slab[T]) slice(n int) []T {
	// for large n, create new slice to avoid fragmentation.
	if n >= cap(s.allocs)/2 {
		return make([]T, n)
	}
	if len(s.allocs)+n > cap(s.allocs) {
		s.allocs = make([]T, 0, cap(s.allocs))
	}
	i := len(s.allocs)
	s.allocs = s.allocs[:i+n]
	return s.allocs[i : i+n : i+n]
}
