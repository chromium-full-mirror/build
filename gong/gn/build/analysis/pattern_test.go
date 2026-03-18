// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"testing"
)

func TestPattern_MatchesString(t *testing.T) {
	tests := []struct {
		pattern   string
		candidate string
		want      bool
	}{
		// Empty pattern matches only empty string.
		{"", "", true},
		{"", "foo", false},
		// Exact matches.
		{"foo", "foo", true},
		{"foo", "bar", false},
		// Path boundaries.
		{`\b`, "", true},
		{`\b`, "/", true},
		{`\b\b`, "", false},
		{`\b\b`, "/", true},
		{`\b\b\b`, "", false},
		{`\b\b\b`, "/", true},
		{`\b`, "//", false},
		{`\bfoo\b`, "foo", true},
		{`\bfoo\b`, "/foo/", true},
		{`\b\bfoo`, "/foo", true},
		// *
		{"*", "", true},
		{"*", "foo", true},
		{"*foo", "foo", true},
		{"*foo", "gagafoo", true},
		{"*foo", "gagafoob", false},
		{"foo*", "foo", true},
		{"foo*", "foogaga", true},
		{"foo*", "bfoogaga", false},
		{"foo*bar", "foobar", true},
		{"foo*bar", "foo-bar", true},
		{"foo*bar", "foolalalalabar", true},
		{"foo*bar", "foolalalalabaz", false},
		{"*a*b*c*d*", "abcd", true},
		{"*a*b*c*d*", "1a2b3c4d5", true},
		{"*a*b*c*d*", "1a2b3c45", false},
		{`*\b\b`, "", false},
		{`\b\b*`, "", false},
		{`*\b\b*`, "", false},
		{`*\bfoo\b*`, "foo", true},
		{`*\bfoo\b*`, "/foo/", true},
		{`*\bfoo\b*`, "foob", false},
		{`*\bfoo\b*`, "lala/foo/bar/baz", true},
	}

	for _, tc := range tests {
		got := MakePattern(tc.pattern).MatchString(tc.candidate)
		if got != tc.want {
			t.Errorf("MakePattern(%q).MatchString(%q) = %v, want %v", tc.pattern, tc.candidate, got, tc.want)
		}
	}
}
