// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package parse

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"infra/build/gong/gn/fs"
	"infra/build/gong/gn/syntax"
)

func TestParse_Simple(t *testing.T) {
	// Directly compare with expected ParseNode output for smaller test cases.
	// For more complex comparisons, particularly ones that go-cmp do not
	// support without a custom comparator (e.g. nodes that contain a
	// []ParseNode will fail to be compared as expected because the cmpOpts
	// IgnoreFields here won't work), use TestParse_Large.
	// TestParse_Large uses AST text dumps, which are easier to write and
	// understand for more complex test cases.
	cmpOpts := []cmp.Option{
		cmp.AllowUnexported(syntax.Token{}),
		cmpopts.IgnoreFields(syntax.Token{}, "location"),
	}
	for _, tc := range []struct {
		name     string
		input    string
		expected ParseNode
	}{
		{
			name:  "empty",
			input: "",
			expected: &BlockNode{
				ResultMode: DiscardsResult,
			},
		},
		{
			name:  "identifier",
			input: "foo",
			expected: &BlockNode{
				ResultMode: DiscardsResult,
				Statements: []ParseNode{
					&IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "foo")},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputPath := filepath.Join(t.TempDir(), "test.gni")
			if err := os.WriteFile(inputPath, []byte(tc.input), 0644); err != nil {
				t.Fatal(err)
			}
			input, err := fs.NewInputFile("/test", inputPath)
			if err != nil {
				t.Fatal(err)
			}
			tokens, err := syntax.Tokenize(input)
			if err != nil {
				t.Errorf("Tokenize(_) = nil, %v; want nil error", err)
			}

			got, err := Parse(tokens)
			if err != nil {
				t.Fatal(err)
			}

			if diff := cmp.Diff(tc.expected, got, cmpOpts...); diff != "" {
				t.Errorf("Parse(_); diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParse_Large(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:  "empty",
			input: "",
			expected: `BLOCK
`,
		},
		{
			name:  "identifier",
			input: "foo",
			expected: `BLOCK
 IDENTIFIER(foo)
`,
		},
		{
			name:  "literal",
			input: "123",
			expected: `BLOCK
 LITERAL(123)
`,
		},
		{
			name:  "call_empty",
			input: "foo()",
			expected: `BLOCK
 FUNCTION(foo)
  LIST
`,
		},
		{
			name:  "call_args",
			input: `foo(1, "a")`,
			expected: `BLOCK
 FUNCTION(foo)
  LIST
   LITERAL(1)
   LITERAL("a")
`,
		},
		{
			name: "call_block",
			input: `foo() {
    bar()
}`,
			expected: `BLOCK
 FUNCTION(foo)
  LIST
  BLOCK
   FUNCTION(bar)
    LIST
`,
		},
		{
			name:  "bracket_expr",
			input: "[foo]",
			expected: `BLOCK
 LIST
  IDENTIFIER(foo)
`,
		},
		{
			name:  "unary_expr",
			input: "!false",
			expected: `BLOCK
 UNARY_OP(!)
  LITERAL(false)
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputPath := filepath.Join(t.TempDir(), "test.gni")
			if err := os.WriteFile(inputPath, []byte(tc.input), 0644); err != nil {
				t.Fatal(err)
			}
			input, err := fs.NewInputFile("/test", inputPath)
			if err != nil {
				t.Fatal(err)
			}
			tokens, err := syntax.Tokenize(input)
			if err != nil {
				t.Errorf("Tokenize(_) = nil, %v; want nil error", err)
			}

			parsed, err := Parse(tokens)
			if err != nil {
				t.Fatal(err)
			}

			var buf strings.Builder
			err = RenderDump(&buf, parsed.Dump())
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.expected, buf.String()); diff != "" {
				t.Errorf("Parse(_); diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParse_Invalid(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  string
		line   int
		column int
	}{
		{
			name:   "plus_after_num",
			input:  "123+",
			line:   1,
			column: 4,
		},
		{
			name:   "hanging_if",
			input:  "if",
			line:   1,
			column: 1,
		},
		{
			name:   "hanging_bracket",
			input:  "[test",
			line:   1,
			column: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputPath := filepath.Join(t.TempDir(), "test.gni")
			if err := os.WriteFile(inputPath, []byte(tc.input), 0644); err != nil {
				t.Fatal(err)
			}
			input, err := fs.NewInputFile("/test", inputPath)
			if err != nil {
				t.Fatal(err)
			}
			tokens, err := syntax.Tokenize(input)
			if err != nil {
				t.Errorf("Tokenize(_) = nil, %v; want nil error", err)
			}

			_, err = Parse(tokens)

			if err == nil {
				t.Errorf("Parse(_) = nil, nil; want error")
			}
			var syntaxErr syntax.Error
			if !errors.As(err, &syntaxErr) {
				t.Errorf("Parse(_) = nil, %v; want syntax.Error", err)
			}
			if syntaxErr.Location().LineNumber() != tc.line || syntaxErr.Location().ColumnNumber() != tc.column {
				t.Errorf("syntax.Error at line = %d, column = %d; want line = %d, column = %d",
					syntaxErr.Location().LineNumber(), syntaxErr.Location().ColumnNumber(),
					tc.line, tc.column)
			}
		})
	}
}
