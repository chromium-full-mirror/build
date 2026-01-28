// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package parse

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"go.chromium.org/build/gong/gn/syntax"
	"go.chromium.org/build/gong/ui"
)

type mockInput struct {
	displayName string
	contents    string
}

func (m mockInput) DisplayName() string { return m.displayName }
func (m mockInput) Contents() []byte    { return []byte(m.contents) }
func (m mockInput) Equal(other syntax.InputSource) bool {
	return bytes.Equal(m.Contents(), other.Contents())
}

func TestParse_Simple(t *testing.T) {
	// Directly compare with expected Node output for smaller test cases.
	// For more complex comparisons, particularly ones that go-cmp do not
	// support without a custom comparator (e.g. nodes that contain a
	// []Node will fail to be compared as expected because the cmpOpts
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
		expected Node
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
				Statements: []Node{
					&IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "foo")},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokens, err := syntax.Tokenize(mockInput{
				displayName: "test.gni",
				contents:    tc.input,
			})
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
			name:  "paren_expr",
			input: "(foo)",
			expected: `BLOCK
 IDENTIFIER(foo)
`,
		},
		{
			name:  "brace_expr",
			input: "{assert(false)}",
			expected: `BLOCK
 BLOCK
  FUNCTION(assert)
   LIST
    LITERAL(false)
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
		{
			name:  "member_accessor",
			input: "a.b",
			expected: `BLOCK
 ACCESSOR
  a
  IDENTIFIER(b)
`,
		},
		{
			name:  "subscript_accessor",
			input: "a[b(c)]",
			expected: `BLOCK
 ACCESSOR
  a
  FUNCTION(b)
   LIST
    IDENTIFIER(c)
`,
		},
		{
			name:  "assign_identifier",
			input: "a = 123",
			expected: `BLOCK
 BINARY(=)
  IDENTIFIER(a)
  LITERAL(123)
`,
		},
		{
			name:  "assign_member",
			input: "a.b = 123",
			expected: `BLOCK
 BINARY(=)
  ACCESSOR
   a
   IDENTIFIER(b)
  LITERAL(123)
`,
		},
		{
			name:  "assign_subscript",
			input: `a[b] = 123`,
			expected: `BLOCK
 BINARY(=)
  ACCESSOR
   a
   IDENTIFIER(b)
  LITERAL(123)
`,
		},
		{
			name:  "add_equals",
			input: `foo += "bar.cc"`,
			expected: `BLOCK
 BINARY(+=)
  IDENTIFIER(foo)
  LITERAL("bar.cc")
`,
		},
		{
			name:  "minus_equals",
			input: `baz -= ["qux.cc"]`,
			expected: `BLOCK
 BINARY(-=)
  IDENTIFIER(baz)
  LIST
   LITERAL("qux.cc")
`,
		},
		{
			name:  "binary_op",
			input: "5 - 1",
			expected: `BLOCK
 BINARY(-)
  LITERAL(5)
  LITERAL(1)
`,
		},
		{
			name:  "binary_op_spaceless",
			input: "5+1",
			expected: `BLOCK
 BINARY(+)
  LITERAL(5)
  LITERAL(1)
`,
		},
		{
			name:  "binary_op_multiple",
			input: "5 - 1 - 2",
			expected: `BLOCK
 BINARY(-)
  BINARY(-)
   LITERAL(5)
   LITERAL(1)
  LITERAL(2)
`,
		},
		{
			name:  "long_expression",
			input: "a = b + c && d || e",
			expected: `BLOCK
 BINARY(=)
  IDENTIFIER(a)
  BINARY(||)
   BINARY(&&)
    BINARY(+)
     IDENTIFIER(b)
     IDENTIFIER(c)
    IDENTIFIER(d)
   IDENTIFIER(e)
`,
		},
		{
			name:  "condition_simple",
			input: "if(1) { a = 2 }",
			expected: `BLOCK
 CONDITION
  LITERAL(1)
  BLOCK
   BINARY(=)
    IDENTIFIER(a)
    LITERAL(2)
`,
		},
		{
			name:  "condition_else_if",
			input: "if(1) { a = 2 } else if (0) { a = 3 } else { a = 4 }",
			expected: `BLOCK
 CONDITION
  LITERAL(1)
  BLOCK
   BINARY(=)
    IDENTIFIER(a)
    LITERAL(2)
  CONDITION
   LITERAL(0)
   BLOCK
    BINARY(=)
     IDENTIFIER(a)
     LITERAL(3)
   BLOCK
    BINARY(=)
     IDENTIFIER(a)
     LITERAL(4)
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokens, err := syntax.Tokenize(mockInput{
				displayName: "test.gni",
				contents:    tc.input,
			})
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
		// TODO: also compare error token/node if available?
		wantErr any
	}{
		{
			name:    "plus_after_num",
			input:   "123+",
			line:    1,
			column:  4,
			wantErr: &EOF{},
		},
		{
			name:    "minus_after_num",
			input:   "123-",
			line:    1,
			column:  4,
			wantErr: &EOF{},
		},
		{
			name:    "hanging_eq",
			input:   "123==",
			line:    1,
			column:  4,
			wantErr: &EOF{},
		},
		{
			name:    "hanging_le",
			input:   "123<=",
			line:    1,
			column:  4,
			wantErr: &EOF{},
		},
		{
			name:    "hanging_ge",
			input:   "123>=",
			line:    1,
			column:  4,
			wantErr: &EOF{},
		},
		{
			name:    "hanging_and",
			input:   "123&&",
			line:    1,
			column:  4,
			wantErr: &EOF{},
		},
		{
			name:    "hanging_or",
			input:   "123||",
			line:    1,
			column:  4,
			wantErr: &EOF{},
		},
		{
			name:    "hanging_if",
			input:   "if",
			line:    1,
			column:  1,
			wantErr: &TokenError{},
		},
		{
			name:    "hanging_bracket",
			input:   "[test",
			line:    1,
			column:  1,
			wantErr: &TokenError{},
		},
		{
			name:    "member_accessor_invalid_lhs",
			input:   "foo().1",
			line:    1,
			column:  1,
			wantErr: &NodeError{},
		},
		{
			name:    "nested_member_accessor",
			input:   "a.b.c",
			line:    1,
			column:  2,
			wantErr: &TokenError{},
		},
		{
			name:    "numeric_member_accessor",
			input:   "a.42",
			line:    1,
			column:  2,
			wantErr: &TokenError{},
		},
		{
			name:    "func_member_accessor",
			input:   "a.cookies()",
			line:    1,
			column:  2,
			wantErr: &TokenError{},
		},
		{
			name:    "assign_literal",
			input:   "123 = a",
			line:    1,
			column:  1,
			wantErr: &NodeError{},
		},
		{
			name:    "assign_func",
			input:   "a() = b",
			line:    1,
			column:  1,
			wantErr: &NodeError{},
		},
		{
			name:    "condition_illegal_assign",
			input:   "if (a=2) {}",
			line:    1,
			column:  5,
			wantErr: &NodeError{},
		},
		{
			name: "condition_missing_braces_if",
			input: `if (true)
  foreach(foo, []) {}
else {
  foreach(bar, []) {}
}`,
			line:    2,
			column:  3,
			wantErr: &TokenError{},
		},
		{
			name: "condition_missing_braces_else",
			input: `if (true) {
  foreach(foo, []) {}
} else
  foreach(bar, []) {}
`,
			line:    4,
			column:  3,
			wantErr: &TokenError{},
		},
		{
			name: "condition_missing_braces_else_if",
			input: `if (true) {
  foreach(foo, []) {}
} else if (true)
  foreach(bar, []) {}
`,
			line:    4,
			column:  3,
			wantErr: &TokenError{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokens, err := syntax.Tokenize(mockInput{
				displayName: "test.gni",
				contents:    tc.input,
			})
			if err != nil {
				t.Errorf("Tokenize(_) = nil, %v; want nil error", err)
			}

			_, err = Parse(tokens)

			if !errors.As(err, tc.wantErr) {
				t.Errorf("Parse(_) = %v (%T), want %T", err, err, tc.wantErr)
			}
			var sourceError ui.PresentableSourceError
			if !errors.As(err, &sourceError) {
				t.Errorf("Parse(_) = %v (%T), want ui.PresentableSourceError", err, err)
			}
			if sourceError.Location().LineNumber() != tc.line || sourceError.Location().ColumnNumber() != tc.column {
				t.Errorf("source error at line = %d, column = %d; want line = %d, column = %d",
					sourceError.Location().LineNumber(), sourceError.Location().ColumnNumber(),
					tc.line, tc.column)
			}
		})
	}
}
