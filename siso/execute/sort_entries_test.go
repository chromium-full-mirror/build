// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execute

import (
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"testing"

	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/merkletree"
)

// TestSortEntriesByName checks that sortEntriesByName orders entries exactly
// like sort.Slice, including entries with equal names (e.g. a directory and
// a symlink), whose order affects the input root.
func TestSortEntriesByName(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	for iter := range 2000 {
		n := rnd.Intn(300)
		ents := make([]merkletree.Entry, 0, n+1)
		ents = append(ents, merkletree.Entry{Name: path.Path(fmt.Sprintf("d%d", rnd.Intn(20)))})
		for i := range n {
			name := path.Path(fmt.Sprintf("d%d/f%d", rnd.Intn(20), rnd.Intn(n/2+1)))
			if rnd.Intn(5) == 0 {
				name = path.Path(fmt.Sprintf("d%d", rnd.Intn(20)))
			}
			// Target distinguishes entries with equal names.
			ents = append(ents, merkletree.Entry{Name: name, Target: fmt.Sprintf("t%d", i)})
		}
		if iter%3 == 0 {
			// mostly sorted input, like real input lists.
			sort.SliceStable(ents[1:], func(i, j int) bool { return ents[1+i].Name < ents[1+j].Name })
		}
		want := append([]merkletree.Entry(nil), ents...)
		sort.Slice(want, func(i, j int) bool { return want[i].Name < want[j].Name })
		got := append([]merkletree.Entry(nil), ents...)
		sortEntriesByName(got)
		for i := range want {
			if want[i].Name != got[i].Name || want[i].Target != got[i].Target {
				t.Fatalf("iter %d: entry %d = %q %q; want %q %q", iter, i, got[i].Name, got[i].Target, want[i].Name, want[i].Target)
			}
		}
	}
}

// BenchmarkSortEntriesByName sorts input entry lists like inputTree: the
// working directory entry first, followed by mostly sorted inputs, or
// followed by inputs in random order.
func BenchmarkSortEntriesByName(b *testing.B) {
	sorted := []merkletree.Entry{{Name: "out/soong/.intermediates/x"}}
	for i := range 2000 {
		sorted = append(sorted, merkletree.Entry{Name: path.Path(fmt.Sprintf("frameworks/base/core/java/f%04d.java", i))})
	}
	shuffled := slices.Clone(sorted)
	rand.New(rand.NewSource(1)).Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	for _, bc := range []struct {
		name string
		ents []merkletree.Entry
	}{
		{"mostly_sorted", sorted},
		{"shuffled", shuffled},
	} {
		b.Run(bc.name, func(b *testing.B) {
			ents := make([]merkletree.Entry, len(bc.ents))
			b.ReportAllocs()
			for b.Loop() {
				copy(ents, bc.ents)
				sortEntriesByName(ents)
			}
		})
	}
}
