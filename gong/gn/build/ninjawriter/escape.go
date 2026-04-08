// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"strings"
)

var scriptRuleNormalizer = strings.NewReplacer(
	":", "_",
	"/", "_",
	"(", "_",
	")", "_",
	"+", "_",
)

// Ninja's escaping rules are very simple. We always escape colons even
// though they're OK in many places, in case the resulting string is used on
// the left-hand-side of a rule.
func shouldEscapeCharForNinja(ch byte) bool {
	return ch == '$' || ch == ' ' || ch == ':'
}

// escapeStringNinja escapes Ninja string characters ($, space, :) for paths and target names.
// Use escapeNinjaCommandPosix instead for strings that appear in command lines.
func escapeStringNinja(str string) string {
	var sb strings.Builder
	for i := range len(str) {
		if shouldEscapeCharForNinja(str[i]) {
			sb.WriteByte('$')
		}
		sb.WriteByte(str[i])
	}
	return sb.String()
}

// Based on C++ GN's kShellValid lookup table.
// https://source.chromium.org/gn/gn/+/main:src/gn/escape.cc;l=25-43;drc=ab638bd7cbb9ac8468bf2fbe60c74ed4706a14a7
func shouldEscapeForShell(ch byte) bool {
	switch {
	case ch >= 'a' && ch <= 'z':
		return false
	case ch >= 'A' && ch <= 'Z':
		return false
	case ch >= '0' && ch <= '9':
		return false
	case ch == '+', ch == ',', ch == '-', ch == '.', ch == '/', ch == '=', ch == '_', ch == '@', ch == ':':
		return false
	}
	return true
}

// escapeNinjaCommandPosix escapes a string for use in a Ninja command line on POSIX systems.
// You probably want escapeStringNinja instead if the string isn't for a command line.
// TODO: windows?
func escapeNinjaCommandPosix(str string) string {
	var sb strings.Builder
	for i := range len(str) {
		ch := str[i]
		if ch == '$' || ch == ' ' {
			// Space and $ are special to both Ninja and the shell. '$' escape for
			// Ninja, then backslash-escape for the shell.
			sb.WriteByte('\\')
			sb.WriteByte('$')
			sb.WriteByte(ch)
		} else if ch == ':' {
			// Colon is the only other Ninja special char, which is not special to
			// the shell.
			sb.WriteByte('$')
			sb.WriteByte(':')
		} else if ch >= 0x80 || shouldEscapeForShell(ch) {
			// All other invalid shell chars get backslash-escaped.
			sb.WriteByte('\\')
			sb.WriteByte(ch)
		} else {
			// Everything else is a literal.
			sb.WriteByte(ch)
		}
	}
	return sb.String()
}
