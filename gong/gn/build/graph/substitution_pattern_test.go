// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestMakeSubstitutionPattern_Valid(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  []SubstitutionPart
	}{
		{
			name:  "literal",
			input: "This is a literal",
			want: []SubstitutionPart{
				SubstitutionLiteral{"This is a literal"},
			},
		},
		{
			name:  "complex",
			input: "AA{{source}}BB{{output}}",
			want: []SubstitutionPart{
				SubstitutionLiteral{"AA"},
				substitutionSource,
				SubstitutionLiteral{"BB"},
				substitutionOutput,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := makeSubstitutionPattern(tc.input)

			if err != nil {
				t.Fatalf("MakeSubstitutionPattern(%q) got err=%v (%T), want nil err", tc.input, err, err)
			}
			if diff := cmp.Diff(tc.want, got.Pattern); diff != "" {
				t.Errorf("MakeSubstitutionPattern(%q); diff (-want +got):\n%s", tc.input, diff)
			}
		})
	}
}

func TestMakeSubstitutionPattern_Invalid(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{
			name:  "unclosed",
			input: "AA{{source",
		},
		{
			name:  "unknown",
			input: "{{source_of_evil}}",
		},
		{
			name:  "nested",
			input: "{{source{{source}}",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := makeSubstitutionPattern(tc.input)

			if e, ok := errors.AsType[SubstitutionFormatError](err); !ok {
				t.Errorf("MakeSubstitutionPattern(%q) got err=%v (%T), wantErr %T", tc.input, err, err, e)
			}
		})
	}
}
