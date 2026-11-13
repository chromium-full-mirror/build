// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package rawstring defines an analyzer that suggests raw string literals for
// multi-line text written as interpreted string literals.
package rawstring

import (
	"go/ast"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/tools/go/analysis"
)

// Analyzer reports interpreted string literals that hold multi-line text and
// could be written as raw string literals instead.
var Analyzer = &analysis.Analyzer{
	Name: "rawstring",
	Doc: `suggests raw string literals for multi-line strings

Reports interpreted ("...") string literals with at least 3 lines of content
that could be written as raw (` + "`...`" + `) string literals. Blank lines,
lines that are only printf verbs and bare "label:" lines don't count as
content. Literals that a raw string can't express faithfully are skipped.

Add //goanalyzer:ignore rawstring on the same or preceding line to suppress a
report.`,
	Run: run,
}

// minContentLines is the number of content lines needed before a literal is
// considered multi-line text rather than, e.g., a format string with
// separators.
const minContentLines = 3

var (
	// fmtVerb matches printf verbs like %s, %-6d, %#v and %%.
	fmtVerb = regexp.MustCompile(`%[-+# 0-9.*\[\]]*[a-zA-Z%]`)
	// label matches lines that are only a short label, e.g. "stdout:".
	label = regexp.MustCompile(`^\s*[\w-]+:\s*$`)
)

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		if ast.IsGenerated(file) {
			continue
		}
		ignored := ignoredLines(pass.Fset, file)
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || !strings.HasPrefix(lit.Value, `"`) {
				return true
			}
			line := pass.Fset.Position(lit.Pos()).Line
			if ignored[line] || ignored[line-1] {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || !convertible(s) || contentLines(s) < minContentLines {
				return true
			}
			pass.Reportf(lit.Pos(), "multi-line string; use a raw string literal (or add //goanalyzer:ignore rawstring)")
			return true
		})
	}
	return nil, nil
}

// contentLines counts the lines of s that carry text, ignoring blank lines,
// lines that are only printf verbs, and bare "label:" lines.
func contentLines(s string) int {
	if !strings.Contains(s, "\n") {
		return 0
	}
	n := 0
	for l := range strings.SplitSeq(s, "\n") {
		l = fmtVerb.ReplaceAllString(l, "")
		if strings.TrimSpace(l) == "" || label.MatchString(l) {
			continue
		}
		n++
	}
	return n
}

// convertible reports whether s can be written as a raw string literal
// without losing information or introducing invisible characters.
func convertible(s string) bool {
	if !utf8.ValidString(s) || strings.Contains(s, "`") {
		return false
	}
	for _, r := range s {
		if r != '\n' && !unicode.IsPrint(r) {
			return false // e.g. \r, \t, \x00, U+00A0, U+200B.
		}
	}
	// Editors commonly strip trailing spaces.
	for l := range strings.SplitSeq(s, "\n") {
		if strings.HasSuffix(l, " ") {
			return false
		}
	}
	return true
}

// ignoredLines returns the lines of f that have the ignore directive.
func ignoredLines(fset *token.FileSet, f *ast.File) map[int]bool {
	m := map[int]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			d, ok := ast.ParseDirective(c.Slash, c.Text)
			if !ok || d.Tool != "goanalyzer" || d.Name != "ignore" {
				continue
			}
			// The first argument names the analyzer; the rest is a reason.
			if args := strings.Fields(d.Args); len(args) > 0 && args[0] == "rawstring" {
				m[fset.Position(c.Pos()).Line] = true
			}
		}
	}
	return m
}
