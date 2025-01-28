// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package parse

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"infra/build/gong/gn/fs"
	"infra/build/gong/gn/syntax"
)

// TODO: implement AST serialization. we don't care about the exact particular
// data structures, we only care about what the AST looks like.
// so we want something like this:
// https://source.chromium.org/gn/gn/+/main:src/gn/parser_unittest.cc;l=132;drc=5649cccd7a4845d504b594816b8b4d861cdbeaf3
func TestParse_Valid(t *testing.T) {
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
		{
			name:  "literal",
			input: "123",
			expected: &BlockNode{
				ResultMode: DiscardsResult,
				Statements: []ParseNode{
					&LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "123")},
				},
			},
		},
		{
			name:  "call_empty",
			input: "foo()",
			expected: &BlockNode{
				ResultMode: DiscardsResult,
				Statements: []ParseNode{
					&FunctionCallNode{
						Function: syntax.MakeToken(syntax.TokenIdentifier, "foo"),
						// Match GN behavior of empty call resulting in ListNode using identifier as begin and end.
						Args: &ListNode{
							BeginToken: syntax.MakeToken(syntax.TokenIdentifier, "foo"),
							End:        EndNode{syntax.MakeToken(syntax.TokenIdentifier, "foo")},
						},
					},
				},
			},
		},
		{
			name:  "call_args",
			input: `foo(1, "a")`,
			expected: &BlockNode{
				ResultMode: DiscardsResult,
				Statements: []ParseNode{
					&FunctionCallNode{
						Function: syntax.MakeToken(syntax.TokenIdentifier, "foo"),
						Args: &ListNode{
							BeginToken: syntax.MakeToken(syntax.TokenLeftParen, "("),
							End:        EndNode{syntax.MakeToken(syntax.TokenRightParen, ")")},
							Contents: []ParseNode{
								&LiteralNode{syntax.MakeToken(syntax.TokenInteger, "1")},
								&LiteralNode{syntax.MakeToken(syntax.TokenString, `"a"`)},
							},
						},
					},
				},
			},
		},
		{
			name: "call_block",
			input: `foo() {
    bar()
}`,
			expected: &BlockNode{
				ResultMode: DiscardsResult,
				Statements: []ParseNode{
					&FunctionCallNode{
						Function: syntax.MakeToken(syntax.TokenIdentifier, "foo"),
						// Match GN behavior of empty call resulting in ListNode using identifier as begin and end.
						Args: &ListNode{
							BeginToken: syntax.MakeToken(syntax.TokenIdentifier, "foo"),
							End:        EndNode{syntax.MakeToken(syntax.TokenIdentifier, "foo")},
						},
						Block: &BlockNode{
							ResultMode: 1,
							BeginToken: syntax.MakeToken(syntax.TokenLeftBrace, "{"),
							End:        EndNode{syntax.MakeToken(syntax.TokenRightBrace, "}")},
							Statements: []ParseNode{
								&FunctionCallNode{
									Function: syntax.MakeToken(syntax.TokenIdentifier, "bar"),
									// Match GN behavior of empty call resulting in ListNode using identifier as begin and end.
									Args: &ListNode{
										BeginToken: syntax.MakeToken(syntax.TokenIdentifier, "bar"),
										End:        EndNode{syntax.MakeToken(syntax.TokenIdentifier, "bar")},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name:  "bracket_expr",
			input: "[foo]",
			expected: &BlockNode{
				ResultMode: DiscardsResult,
				Statements: []ParseNode{
					&ListNode{
						BeginToken: syntax.MakeToken(syntax.TokenLeftBracket, "["),
						End:        EndNode{syntax.MakeToken(syntax.TokenRightBracket, "]")},
						Contents: []ParseNode{
							&IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "foo")},
						},
					},
				},
			},
		},
		{
			name:  "unary_expr",
			input: "!false",
			expected: &BlockNode{
				ResultMode: DiscardsResult,
				Statements: []ParseNode{
					&UnaryOpNode{
						Op: syntax.MakeToken(syntax.TokenBang, "!"),
						Operand: &LiteralNode{
							Token: syntax.MakeToken(syntax.TokenFalse, "false"),
						},
					},
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
