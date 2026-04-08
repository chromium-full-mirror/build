// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"testing"
)

func TestEscapeStringNinja(t *testing.T) {
	result := escapeStringNinja(`asdf: "$\bar`)
	if result != `asdf$:$ "$$\bar` {
		t.Errorf("got %q, want %q", result, `asdf$:$ "$$\bar`)
	}
}

func TestEscapeNinjaCommandPosix(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{
			input: `a: "$\b`,
			want:  `a$:\$ \"\$$\\b`,
		},
		{
			input: `a_;<*b`,
			want:  `a_\;\<\*b`,
		},
		{
			input: `{a,b}{c,d}`,
			want:  `\{a,b\}\{c,d\}`,
		},
	} {
		got := escapeNinjaCommandPosix(tc.input)
		if got != tc.want {
			t.Errorf("escapeNinjaCommandPosix(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}
