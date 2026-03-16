// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"strings"
)

// Ninja's escaping rules are very simple. We always escape colons even
// though they're OK in many places, in case the resulting string is used on
// the left-hand-side of a rule.
func shouldEscapeCharForNinja(ch byte) bool {
	return ch == '$' || ch == ' ' || ch == ':'
}

// escapeStringNinja escapes Ninja string characters ($, space, :).
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
