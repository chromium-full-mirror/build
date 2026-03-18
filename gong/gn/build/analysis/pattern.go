// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import "strings"

const PatternsHelp = `File patterns

  File patterns are VERY limited regular expressions. They must match the
  entire input string to be counted as a match. In regular expression parlance,
  there is an implicit "^...$" surrounding your input. If you want to match a
  substring, you need to use wildcards at the beginning and end.

  There are only two special tokens understood by the pattern matcher.
  Everything else is a literal.

   - "*" Matches zero or more of any character. It does not depend on the
     preceding character (in regular expression parlance it is equivalent to
     ".*").

   - "\b" Matches a path boundary. This will match the beginning or end of a
     string, or a slash.

Pattern examples

  "*asdf*"
      Matches a string containing "asdf" anywhere.

  "asdf"
      Matches only the exact string "asdf".

  "*.cc"
      Matches strings ending in the literal ".cc".

  "\bwin/*"
      Matches "win/foo" and "foo/win/bar.cc" but not "iwin/foo".
`

// Pattern represents a parsed GN file pattern used for matching file paths.
//
// This cannot be implemented using Go's regexp package. Unlike regex's \b,
// which matches a generic word boundary, GN's \b specifically matches a path
// boundary: either a slash, the beginning of the string, or the end of the
// string. A naive regex implementation would therefore translate GN's \b to
// the non-capturing regex group (?:^|/|$).
//
// However, take the following pattern matching against the empty string "":
//
//	\b\b\b
//
// If we evaluated this using the naive regex, it would incorrectly return
// true by evaluating to ^^^. Go's regexp package treats ^ and $ as zero-width
// assertions that can be matched infinitely without advancing the cursor.
// By contrast, GN requires that conceptual string boundaries be "consumed"
// exactly once, so \b\b\b fails against an empty string.
type Pattern struct {
	tokens []token
}

type tokenType int

const (
	literal tokenType = iota
	wildcard
	pathBoundary
)

type token struct {
	typ tokenType
	val string
}

// MakePattern parses a pattern string and returns a Pattern.
func MakePattern(p string) Pattern {
	var tokens []token
	literalStart := -1

	for i := 0; i < len(p); i++ {
		switch {
		case p[i] == '*':
			if literalStart != -1 {
				tokens = append(tokens, token{literal, p[literalStart:i]})
				literalStart = -1
			}
			tokens = append(tokens, token{wildcard, ""})

		case strings.HasPrefix(p[i:], "\\b"):
			if literalStart != -1 {
				tokens = append(tokens, token{literal, p[literalStart:i]})
				literalStart = -1
			}
			tokens = append(tokens, token{pathBoundary, ""})
			i++ // skip past 'b'

		default:
			if literalStart == -1 {
				literalStart = i
			}
		}
	}
	if literalStart != -1 {
		tokens = append(tokens, token{literal, p[literalStart:]})
	}

	return Pattern{tokens}
}

// MatchString reports whether the string s contains any match of the pattern.
func (p Pattern) MatchString(s string) bool {
	return match(p.tokens, s, cursor{idx: 0})
}

type cursor struct {
	idx              int
	consumedBoundary bool
}

func match(tokens []token, str string, c cursor) bool {
	if len(tokens) == 0 {
		return c.idx == len(str)
	}

	tok := tokens[0]
	nextTokens := tokens[1:]

	switch tok.typ {
	case literal:
		endIdx := c.idx + len(tok.val)
		if endIdx <= len(str) && str[c.idx:endIdx] == tok.val {
			// Literal resets the consumed boundary state
			return match(nextTokens, str, cursor{idx: endIdx})
		}

	case wildcard:
		for i := c.idx; i <= len(str); i++ {
			// Wildcard resets the consumed boundary state
			if match(nextTokens, str, cursor{idx: i}) {
				return true
			}
		}

	case pathBoundary:
		// 1. Implicit boundary (start or end of string)
		if !c.consumedBoundary && (c.idx == 0 || c.idx == len(str)) {
			// Recurse with consumedBoundary = true
			if match(nextTokens, str, cursor{idx: c.idx, consumedBoundary: true}) {
				return true
			}
		}

		// 2. Explicit boundary (literal slash)
		if c.idx < len(str) && str[c.idx] == '/' {
			// Slash resets the consumed boundary state
			if match(nextTokens, str, cursor{idx: c.idx + 1}) {
				return true
			}
		}
	}

	return false
}
