// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/siso/path"
)

func TestUniqueFiles(t *testing.T) {
	testCases := []struct {
		name  string
		input [][]string
		want  []string
	}{
		{
			name:  "empty",
			input: [][]string{},
			want:  []string{},
		},
		{
			name:  "one_slice_no_duplicates",
			input: [][]string{{"a", "b", "c"}},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "one_slice_with_duplicates",
			input: [][]string{{"a", "b", "a"}},
			want:  []string{"a", "b"},
		},
		{
			name:  "one_slice_with_empty_string",
			input: [][]string{{"a", "", "b"}},
			want:  []string{"a", "b"},
		},
		{
			name:  "multiple_slices_no_overlap",
			input: [][]string{{"a", "b"}, {"c", "d"}},
			want:  []string{"a", "b", "c", "d"},
		},
		{
			name:  "multiple_slices_with_overlap",
			input: [][]string{{"a", "b"}, {"b", "c"}},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "multiple_slices_with_empty_strings_and_duplicates",
			input: [][]string{{"a", ""}, {"b", "a", ""}, {"c"}},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "all_empty",
			input: [][]string{{""}, {""}},
			want:  []string{},
		},
		{
			name:  "nil_input",
			input: nil,
			want:  []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// TODO(b/477751081): The order of results is important for deps-log correctness.
			// Investigate why changing the order breaks the deps log and fix it.
			// After that, uniqueFiles can be optimized for memory usage.
			got := uniqueFiles(tc.input...)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("uniqueFiles() returned diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUniquePathFiles(t *testing.T) {
	testCases := []struct {
		name  string
		input [][]path.Path
		want  []path.Path
	}{
		{
			name:  "empty",
			input: [][]path.Path{},
			want:  []path.Path{},
		},
		{
			name:  "one_slice_no_duplicates",
			input: [][]path.Path{{"a", "b", "c"}},
			want:  []path.Path{"a", "b", "c"},
		},
		{
			name:  "one_slice_with_duplicates",
			input: [][]path.Path{{"a", "b", "a"}},
			want:  []path.Path{"a", "b"},
		},
		{
			name:  "one_slice_with_empty_string",
			input: [][]path.Path{{"a", "", "b"}},
			want:  []path.Path{"a", "b"},
		},
		{
			name:  "one_slice_with_leading_empty_string",
			input: [][]path.Path{{"", "a", "b"}},
			want:  []path.Path{"a", "b"},
		},
		{
			name:  "one_slice_with_trailing_empty_string",
			input: [][]path.Path{{"a", "b", ""}},
			want:  []path.Path{"a", "b"},
		},
		{
			name:  "multiple_slices_no_overlap",
			input: [][]path.Path{{"a", "b"}, {"c", "d"}},
			want:  []path.Path{"a", "b", "c", "d"},
		},
		{
			name:  "multiple_slices_with_overlap",
			input: [][]path.Path{{"a", "b"}, {"b", "c"}},
			want:  []path.Path{"a", "b", "c"},
		},
		{
			name:  "multiple_slices_with_empty_strings_and_duplicates",
			input: [][]path.Path{{"a", ""}, {"b", "a", ""}, {"c"}},
			want:  []path.Path{"a", "b", "c"},
		},
		{
			name:  "mostly_duplicates",
			input: [][]path.Path{{"a", "b"}, {"a", "b", "a", "b", "a"}},
			want:  []path.Path{"a", "b"},
		},
		{
			name:  "empty_slices",
			input: [][]path.Path{{}, nil},
			want:  []path.Path{},
		},
		{
			name:  "all_empty",
			input: [][]path.Path{{""}, {""}},
			want:  []path.Path{},
		},
		{
			name:  "nil_input",
			input: nil,
			want:  []path.Path{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.input)
			for i := range before {
				before[i] = slices.Clone(before[i])
			}
			got := uniquePathFiles(tc.input...)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("uniquePathFiles() returned diff (-want +got):\n%s", diff)
			}
			if cap(got) > 2*len(got) {
				t.Errorf("uniquePathFiles() cap=%d len=%d; result retains too much", cap(got), len(got))
			}
			for i := range got {
				got[i] = "changed"
			}
			if diff := cmp.Diff(before, tc.input); diff != "" {
				t.Errorf("writing to the result changed the inputs (-before +after):\n%s", diff)
			}
		})
	}
}

// BenchmarkUniquePathFiles merges deps base inputs with depfile inputs,
// as depsCmd does.
func BenchmarkUniquePathFiles(b *testing.B) {
	var base, deps []path.Path
	for i := range 3000 {
		base = append(base, path.Path(fmt.Sprintf("frameworks/base/include/h%04d.h", i)))
	}
	for i := range 2000 {
		deps = append(deps, path.Path(fmt.Sprintf("frameworks/base/include/h%04d.h", 2*i)))
	}
	b.ReportAllocs()
	for b.Loop() {
		uniquePathFiles(base, deps)
	}
}
