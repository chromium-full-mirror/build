// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package parse

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"infra/build/gong/gn/syntax"
)

func TestDump(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input ParseNode
		want  NodeDump
	}{
		{
			name: "accessor_member",
			input: &AccessorNode{
				Base: syntax.MakeToken(syntax.TokenIdentifier, "foo"),
				Member: &IdentifierNode{
					Value: syntax.MakeToken(syntax.TokenIdentifier, "bar"),
				},
			},
			want: NodeDump{
				Type:  "ACCESSOR",
				Value: "foo",
				Children: []NodeDump{
					{
						Type:  "IDENTIFIER",
						Value: "bar",
					},
				},
				AccessorKind: "member",
			},
		},
		{
			name: "accessor_subscript",
			input: &AccessorNode{
				Base: syntax.MakeToken(syntax.TokenIdentifier, "foo"),
				Subscript: &UnaryOpNode{
					Op: syntax.MakeToken(syntax.TokenBang, "!"),
					Operand: &IdentifierNode{
						Value: syntax.MakeToken(syntax.TokenIdentifier, "bar"),
					},
				},
			},
			want: NodeDump{
				Type:  "ACCESSOR",
				Value: "foo",
				Children: []NodeDump{
					{
						Type:  "UNARY_OP",
						Value: "!",
						Children: []NodeDump{
							{
								Type:  "IDENTIFIER",
								Value: "bar",
							},
						},
					},
				},
				AccessorKind: "subscript",
			},
		},
		{
			name: "braces_int",
			input: &BlockNode{
				BeginToken: syntax.MakeToken(syntax.TokenLeftBrace, "{"),
				End: EndNode{
					Value: syntax.MakeToken(syntax.TokenRightBrace, "}"),
				},
				Statements: []ParseNode{&LiteralNode{
					Token: syntax.MakeToken(syntax.TokenInteger, "1"),
				}},
			},
			want: NodeDump{
				Type: "BLOCK",
				Children: []NodeDump{
					{
						Type:  "LITERAL",
						Value: "1",
					},
				},
				BeginToken: "{",
				End: &NodeDump{
					Type:  "END",
					Value: "}",
				},
			},
		},
		{
			name: "func_call",
			input: &FunctionCallNode{
				Function: syntax.MakeToken(syntax.TokenIdentifier, "print"),
				Args: &ListNode{
					BeginToken: syntax.MakeToken(syntax.TokenLeftParen, "("),
					End: EndNode{
						Value: syntax.MakeToken(syntax.TokenRightParen, ")"),
					},
				},
			},
			want: NodeDump{
				Type:  "FUNCTION",
				Value: "print",
				Children: []NodeDump{
					{
						Type:       "LIST",
						BeginToken: "(",
						End: &NodeDump{
							Type:  "END",
							Value: ")",
						},
					},
				},
			},
		},
		{
			name: "literal",
			input: &LiteralNode{
				Token: syntax.MakeToken(syntax.TokenInteger, "123"),
			},
			want: NodeDump{
				Type:  "LITERAL",
				Value: "123",
			},
		},
		{
			name: "binary_op",
			input: &BinaryOpNode{
				Op: syntax.MakeToken(syntax.TokenEqual, "="),
				Left: &LiteralNode{
					Token: syntax.MakeToken(syntax.TokenIdentifier, "a"),
				},
				Right: &LiteralNode{
					Token: syntax.MakeToken(syntax.TokenInteger, "123"),
				},
			},
			want: NodeDump{
				Type:  "BINARY_OP",
				Value: "=",
				Children: []NodeDump{
					{
						Type:  "LITERAL",
						Value: "a",
					},
					{
						Type:  "LITERAL",
						Value: "123",
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.input.Dump()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("input.Dump(); diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRenderDumpAsText(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    ParseNode
		want     string
		wantText string
	}{
		{
			name: "literal",
			input: &LiteralNode{
				Token: syntax.MakeToken(syntax.TokenInteger, "123"),
			},
			wantText: "LITERAL(123)\n",
		},
		{
			name: "unary_expr",
			input: &UnaryOpNode{
				Op: syntax.MakeToken(syntax.TokenBang, "!"),
				Operand: &LiteralNode{
					Token: syntax.MakeToken(syntax.TokenFalse, "false"),
				},
			},
			wantText: "UNARY_OP(!)\n LITERAL(false)\n",
		},
		{
			name: "identifier",
			input: &IdentifierNode{
				Value: syntax.MakeToken(syntax.TokenIdentifier, "foo"),
			},
			wantText: "IDENTIFIER(foo)\n",
		},
		{
			name: "accessor_member",
			input: &AccessorNode{
				Base: syntax.MakeToken(syntax.TokenIdentifier, "foo"),
				Member: &IdentifierNode{
					Value: syntax.MakeToken(syntax.TokenIdentifier, "bar"),
				},
			},
			wantText: "ACCESSOR\n foo\n IDENTIFIER(bar)\n",
		},
		{
			name: "accessor_subscript",
			input: &AccessorNode{
				Base: syntax.MakeToken(syntax.TokenIdentifier, "foo"),
				Subscript: &UnaryOpNode{
					Op: syntax.MakeToken(syntax.TokenBang, "!"),
					Operand: &IdentifierNode{
						Value: syntax.MakeToken(syntax.TokenIdentifier, "bar"),
					},
				},
			},
			wantText: "ACCESSOR\n foo\n UNARY_OP(!)\n  IDENTIFIER(bar)\n",
		},
		{
			name: "block_comment",
			input: &BlockCommentNode{
				Comment: syntax.MakeToken(syntax.TokenBlockComment, "# Comment"),
			},
			wantText: "BLOCK_COMMENT(# Comment)\n",
		},
		{
			name: "binary_op",
			input: &BinaryOpNode{
				Op: syntax.MakeToken(syntax.TokenEqual, "="),
				Left: &LiteralNode{
					Token: syntax.MakeToken(syntax.TokenIdentifier, "a"),
				},
				Right: &LiteralNode{
					Token: syntax.MakeToken(syntax.TokenInteger, "123"),
				},
			},
			wantText: "BINARY_OP(=)\n LITERAL(a)\n LITERAL(123)\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf strings.Builder
			err := RenderDump(&buf, tc.input.Dump())
			if err != nil {
				t.Errorf("failed to dump: %v", err)
				return
			}
			if diff := cmp.Diff(tc.wantText, buf.String()); diff != "" {
				t.Errorf("RenderDump(_,buf); buf; diff (-want +got):\n%s", diff)
			}
		})
	}
}
