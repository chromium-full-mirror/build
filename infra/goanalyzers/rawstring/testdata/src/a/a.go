// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package a

import "fmt"

var (
	// Multi-line text is reported.
	helpText = "The thing on the left must be an identifier\nand not an expression.\nSorry." // want `multi-line string; use a raw string literal`
	script   = "#!/bin/sh\ncat '%s'\nexit %d\n"                                              // want `multi-line string; use a raw string literal`
	manifest = "rule cc\n  command = cc\n# trailing"                                         // want `multi-line string; use a raw string literal`

	// Raw strings are fine.
	raw = `line 1
line 2
line 3`

	// Too few content lines.
	singleLine   = "a single line\n"
	twoLines     = "#!/bin/sh\nexit 0\n"
	surrounding  = "\n%6s %s: %v\n"
	blankLines   = "Error: none.\n\n\nUsage: %s\n"
	verbsLabels  = "failed: %v\nstdout:\n%s\nstderr:\n%s\n"
	onlyNewlines = "\n\n\n\n"

	// Not expressible as a raw string.
	crlf          = "foo.cc\r\ncl: warning\r\nbar\r\n"
	tabs          = "rule cc\n\tcommand = cc\n\tdescription = CC\n"
	backquote     = "has `backquote`\nline 2\nline 3"
	trailingSpace = "line 1 \nline 2\nline 3"
	nbsp          = "line\u00a01\nline 2\nline 3"

	// Suppressed.
	sameLine = "line 1\nline 2\nline 3" //goanalyzer:ignore rawstring reason
	//goanalyzer:ignore rawstring reason
	prevLine = "line 1\nline 2\nline 3"

	// Not suppressed: directive for another analyzer.
	//goanalyzer:ignore rawstringx reason
	otherAnalyzer = "line 1\nline 2\nline 3" // want `multi-line string; use a raw string literal`
)

func f() {
	fmt.Println(helpText, script, manifest, raw, singleLine, twoLines,
		surrounding, blankLines, verbsLabels, onlyNewlines, crlf, tabs,
		backquote, trailingSpace, nbsp, sameLine, prevLine, otherAnalyzer)
}
