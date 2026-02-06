// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

func TestExecuteNode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope *Scope
		node  parse.Node
		want  Value
		// TODO: this is a temporary hack to support errors being returned with
		// the deprecated ErrKind type versus strongly-typed errors.
		wantErrKind syntax.ErrKind
		wantErr     any
	}{
		{
			name: "access_undefined_base",
			scope: &Scope{
				// The access should fail because a is not defined.
				values: map[string]record{},
			},
			node: &parse.AccessorNode{
				// a.b
				Base:   syntax.MakeToken(syntax.TokenIdentifier, "a"),
				Member: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "b")},
			},
			wantErr: &KeyError{},
		},
		{
			name: "access_undefined_member",
			scope: &Scope{
				values: map[string]record{
					// Define a as a Scope. It should still fail because b isn't defined.
					"a": {value: &ScopeValue{
						scope: &Scope{
							values: map[string]record{},
						},
					}},
				},
			},
			node: &parse.AccessorNode{
				// a = { }
				// a.b
				Base:   syntax.MakeToken(syntax.TokenIdentifier, "a"),
				Member: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "b")},
			},
			wantErr: &KeyError{},
		},
		{
			name: "access_scope_member",
			scope: &Scope{
				values: map[string]record{
					"a": {value: &ScopeValue{
						scope: &Scope{
							// Define b, accessor should succeed now.
							values: map[string]record{
								"b": {value: &IntegerValue{value: 42}},
							},
						},
					}},
				},
			},
			node: &parse.AccessorNode{
				// a = { b = 42 }
				// a.b
				Base:   syntax.MakeToken(syntax.TokenIdentifier, "a"),
				Member: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "b")},
			},
			want:        &IntegerValue{value: 42},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "access_list_by_subscript",
			scope: &Scope{
				values: map[string]record{
					"a": {value: &ListValue{
						list: []Value{
							&IntegerValue{value: 42},
						},
					}},
				},
			},
			node: &parse.AccessorNode{
				// a = [42]
				// a[0]
				Base:      syntax.MakeToken(syntax.TokenIdentifier, "a"),
				Subscript: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "0")},
			},
			want:        &IntegerValue{value: 42},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "access_list_by_subscript_negative_index",
			scope: &Scope{
				values: map[string]record{
					"a": {value: &ListValue{
						list: []Value{
							&IntegerValue{value: 42},
						},
					}},
				},
			},
			node: &parse.AccessorNode{
				// a = [42]
				// a[-1]
				Base: syntax.MakeToken(syntax.TokenIdentifier, "a"),
				Subscript: &parse.LiteralNode{
					Token: syntax.MakeToken(syntax.TokenInteger, "-1"),
				},
			},
			wantErr: &SubscriptError{},
		},
		{
			name: "access_list_by_subscript_out_of_bounds",
			scope: &Scope{
				values: map[string]record{
					"a": {value: &ListValue{
						list: []Value{
							&IntegerValue{value: 42},
						},
					}},
				},
			},
			node: &parse.AccessorNode{
				// a = [42]
				// a[1]
				Base:      syntax.MakeToken(syntax.TokenIdentifier, "a"),
				Subscript: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
			},
			wantErr: &SubscriptError{},
		},
		{
			name: "access_list_by_subscript_empty_list",
			scope: &Scope{
				values: map[string]record{
					"a": {value: &ListValue{
						list: []Value{},
					}},
				},
			},
			node: &parse.AccessorNode{
				// a = []
				// a[0]
				Base:      syntax.MakeToken(syntax.TokenIdentifier, "a"),
				Subscript: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "0")},
			},
			wantErr: &SubscriptError{},
		},
		{
			name: "access_list_by_subscript_non_integer",
			scope: &Scope{
				values: map[string]record{
					"a": {value: &ListValue{
						list: []Value{
							&IntegerValue{value: 42},
						},
					}},
				},
			},
			node: &parse.AccessorNode{
				// a = [42]
				// a["0"]
				Base:      syntax.MakeToken(syntax.TokenIdentifier, "a"),
				Subscript: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenString, `"0"`)},
			},
			wantErr: &TypeError{},
		},
		{
			name: "access_scope_by_subscript",
			scope: &Scope{
				values: map[string]record{
					"a": {value: &ScopeValue{
						scope: &Scope{
							values: map[string]record{
								"b": {value: &IntegerValue{value: 42}},
							},
						},
					}},
				},
			},
			node: &parse.AccessorNode{
				// a = { b = 42 }
				// a["b"]
				Base:      syntax.MakeToken(syntax.TokenIdentifier, "a"),
				Subscript: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenString, `"b"`)},
			},
			want:        &IntegerValue{value: 42},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "access_scope_by_subscript_undefined_member",
			scope: &Scope{
				values: map[string]record{
					"a": {value: &ScopeValue{
						scope: &Scope{
							values: map[string]record{
								"b": {value: &IntegerValue{value: 42}},
							},
						},
					}},
				},
			},
			node: &parse.AccessorNode{
				// a = { b = 42 }
				// a["c"]
				Base:      syntax.MakeToken(syntax.TokenIdentifier, "a"),
				Subscript: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenString, `"c"`)},
			},
			wantErr: &KeyError{},
		},
		{
			name: "access_scope_by_subscript_non_string",
			scope: &Scope{
				values: map[string]record{
					"a": {value: &ScopeValue{
						scope: &Scope{
							values: map[string]record{
								"b": {value: &IntegerValue{value: 42}},
							},
						},
					}},
				},
			},
			node: &parse.AccessorNode{
				// a = { b = 42 }
				// a[0]
				Base:      syntax.MakeToken(syntax.TokenIdentifier, "a"),
				Subscript: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "0")},
			},
			wantErr: &TypeError{},
		},
		{
			name: "access_by_subscript_invalid_base",
			scope: &Scope{
				values: map[string]record{
					"a": {value: &IntegerValue{value: 1}},
				},
			},
			node: &parse.AccessorNode{
				// a = 1
				// a[0]
				Base:      syntax.MakeToken(syntax.TokenIdentifier, "a"),
				Subscript: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "0")},
			},
			wantErr: &TypeError{},
		},
		{
			name:  "function_call_success",
			scope: &Scope{functions: map[string]FunctionInfo{"mock_func": &mockFunction{value: 42}}},
			node: &parse.FunctionCallNode{
				Function: syntax.MakeToken(syntax.TokenIdentifier, "mock_func"),
				Args: &parse.ListNode{
					Contents: []parse.Node{
						&parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "10")},
					},
				},
			},
			want:        &IntegerValue{value: 52},
			wantErrKind: syntax.ErrNone,
		},
		{
			name:  "function_call_undefined",
			scope: &Scope{functions: map[string]FunctionInfo{}},
			node: &parse.FunctionCallNode{
				Function: syntax.MakeToken(syntax.TokenIdentifier, "mock_func"),
				Args: &parse.ListNode{
					Contents: []parse.Node{},
				},
			},
			wantErr: &UnknownFunctionError{},
		},
		{
			name: "function_call_fails_if_args_cannot_evaluate",
			scope: &Scope{
				functions: map[string]FunctionInfo{"mock_func": &mockFunction{}},
				values:    map[string]record{},
			},
			node: &parse.FunctionCallNode{
				Function: syntax.MakeToken(syntax.TokenIdentifier, "mock_func"),
				Args: &parse.ListNode{
					Contents: []parse.Node{
						// a.b where 'a' is not defined.
						&parse.AccessorNode{
							Base:   syntax.MakeToken(syntax.TokenIdentifier, "a"),
							Member: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "b")},
						},
					},
				},
			},
			// Error should reflect what was encountered evaluating the args.
			wantErr: &KeyError{},
		},
		{
			name: "access_by_subscript_undefined_base",
			scope: &Scope{
				values: map[string]record{},
			},
			node: &parse.AccessorNode{
				// a[0]
				Base:      syntax.MakeToken(syntax.TokenIdentifier, "a"),
				Subscript: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "0")},
			},
			wantErr: &UndefinedIdentifierError{},
		},
		{
			name:        "blockcomment_nothing",
			node:        &parse.BlockCommentNode{},
			scope:       &Scope{},
			wantErrKind: syntax.ErrNone,
		},
		{
			name:        "literal_true",
			node:        &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenTrue, "true")},
			scope:       &Scope{},
			want:        &BooleanValue{value: true},
			wantErrKind: syntax.ErrNone,
		},
		{
			name:        "literal_false",
			node:        &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenFalse, "false")},
			scope:       &Scope{},
			want:        &BooleanValue{value: false},
			wantErrKind: syntax.ErrNone,
		},
		{
			name:        "literal_integer",
			node:        &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "123")},
			scope:       &Scope{},
			want:        &IntegerValue{value: 123},
			wantErrKind: syntax.ErrNone,
		},
		{
			name:        "literal_integer_negative",
			node:        &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "-1")},
			scope:       &Scope{},
			want:        &IntegerValue{value: -1},
			wantErrKind: syntax.ErrNone,
		},
		{
			name:        "literal_integer_negative_zero",
			node:        &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "-0")},
			scope:       &Scope{},
			wantErrKind: syntax.ErrUnknown,
		},
		{
			name:        "literal_integer_leading_zeroes",
			node:        &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "01")},
			scope:       &Scope{},
			wantErrKind: syntax.ErrUnknown,
		},
		{
			name:        "literal_integer_negative_leading_zeroes",
			node:        &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "-01")},
			scope:       &Scope{},
			wantErrKind: syntax.ErrUnknown,
		},
		{
			name:        "literal_integer_invalid",
			node:        &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "123123612836217863781263781263786128371278637821678362817")},
			scope:       &Scope{},
			wantErrKind: syntax.ErrUnknown,
		},
		{
			name:        "literal_string",
			node:        &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenString, `"hello"`)},
			scope:       &Scope{},
			want:        &StringValue{value: "hello"},
			wantErrKind: syntax.ErrNone,
		},
		{
			name:        "literal_string_empty",
			node:        &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenString, `""`)},
			scope:       &Scope{},
			want:        &StringValue{value: ""},
			wantErrKind: syntax.ErrNone,
		},
		{
			name:    "literal_invalid_token",
			node:    &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenPlus, "+")},
			scope:   &Scope{},
			wantErr: &ASTError{},
		},
		{
			// TODO(b/388723392): this is just a smoke test right now because
			// we don't support anything that causes side effects yet.
			// once we do, change this test so that it tests for the correct side effect.
			name: "condition_true",
			node: &parse.ConditionNode{
				IfToken:   syntax.MakeToken(syntax.TokenIf, "if"),
				Condition: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenTrue, "true")},
				IfTrue: &parse.BlockNode{
					Statements: []parse.Node{},
				},
				IfFalse: &parse.BlockNode{
					Statements: []parse.Node{},
				},
			},
			scope:       &Scope{},
			wantErrKind: syntax.ErrNone,
		},
		{
			// TODO(b/388723392): this is just a smoke test right now because
			// we don't support anything that causes side effects yet.
			// once we do, change this test so that it tests for the correct side effect.
			name: "condition_false",
			node: &parse.ConditionNode{
				IfToken:   syntax.MakeToken(syntax.TokenIf, "if"),
				Condition: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenFalse, "false")},
				IfTrue: &parse.BlockNode{
					Statements: []parse.Node{},
				},
				IfFalse: &parse.BlockNode{
					Statements: []parse.Node{},
				},
			},
			scope:       &Scope{},
			wantErrKind: syntax.ErrNone,
		},
		{
			// TODO(b/388723392): this is just a smoke test right now because
			// we don't support anything that causes side effects yet.
			// once we do, change this test so that it tests for the correct side effect.
			name: "condition_true_no_else",
			node: &parse.ConditionNode{
				IfToken:   syntax.MakeToken(syntax.TokenIf, "if"),
				Condition: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenTrue, "true")},
				IfTrue: &parse.BlockNode{
					Statements: []parse.Node{},
				},
			},
			scope:       &Scope{},
			wantErrKind: syntax.ErrNone,
		},
		{
			// TODO(b/388723392): this is just a smoke test right now because
			// we don't support anything that causes side effects yet.
			// once we do, change this test so that it tests for the correct side effect.
			name: "condition_false_no_else",
			node: &parse.ConditionNode{
				IfToken:   syntax.MakeToken(syntax.TokenIf, "if"),
				Condition: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenFalse, "false")},
				IfTrue: &parse.BlockNode{
					Statements: []parse.Node{},
				},
			},
			scope:       &Scope{},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "condition_non_boolean",
			node: &parse.ConditionNode{
				IfToken: syntax.MakeToken(syntax.TokenIf, "if"),
				// Only boolean conditions are supported (no support for "truthy" values).
				Condition: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "123")},
				IfTrue: &parse.BlockNode{
					Statements: []parse.Node{},
				},
			},
			scope:   &Scope{},
			wantErr: &TypeError{},
		},
		{
			name:  "list_empty",
			scope: &Scope{},
			node: &parse.ListNode{
				Contents: []parse.Node{},
			},
			want:        &ListValue{list: []Value{}},
			wantErrKind: syntax.ErrNone,
		},
		{
			name:  "list_simple",
			scope: &Scope{},
			node: &parse.ListNode{
				Contents: []parse.Node{
					&parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
					&parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenTrue, "true")},
					&parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenString, "\"a\"")},
				},
			},
			want: &ListValue{
				list: []Value{
					&IntegerValue{value: 1},
					&BooleanValue{value: true},
					&StringValue{value: "a"},
				},
			},
			wantErrKind: syntax.ErrNone,
		},
		{
			name:  "list_with_comment",
			scope: &Scope{},
			node: &parse.ListNode{
				Contents: []parse.Node{
					&parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
					&parse.BlockCommentNode{},
					&parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenTrue, "true")},
				},
			},
			want: &ListValue{
				list: []Value{
					&IntegerValue{value: 1},
					&BooleanValue{value: true},
				},
			},
			wantErrKind: syntax.ErrNone,
		},
		{
			name:  "list_non_value",
			scope: &Scope{},
			node: &parse.ListNode{
				Contents: []parse.Node{
					&parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
					// A condition node isn't a usable value in a list.
					&parse.ConditionNode{
						IfToken:   syntax.MakeToken(syntax.TokenIf, "if"),
						Condition: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenTrue, "true")},
						IfTrue: &parse.BlockNode{
							BeginToken: syntax.MakeToken(syntax.TokenLeftBrace, "{"),
							End:        parse.EndNode{Value: syntax.MakeToken(syntax.TokenRightBrace, "}")},
						},
					},
				},
			},
			wantErr: &TypeError{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExecuteNode(tc.node, tc.scope)
			wantErr := tc.wantErr != nil || tc.wantErrKind != syntax.ErrNone
			gotErr := err != nil

			if gotErr != wantErr {
				t.Fatalf("ExecuteNode(%T, %T): got err=%v, wantErrKind=%v", tc.node, tc.scope, err, tc.wantErrKind)
			}

			// If error is expected, then check kind matches.
			if gotErr {
				// TODO: temporary hack to support both wantErr and wantErrKind tests.
				if tc.wantErr != nil {
					if !errors.As(err, tc.wantErr) {
						t.Errorf("ExecuteNode(%T, %T): got err=%v (%T), want %T", tc.node, tc.scope, err, err, tc.wantErr)
					}
				} else {
					if match, gotErrKind := syntax.AsErrKind(err, tc.wantErrKind); match == nil {
						t.Fatalf("ExecuteNode(%T, %T): got err=%v (kind %s), wantErrKind=%s", tc.node, tc.scope, err, gotErrKind, tc.wantErrKind)
					}
				}
				return
			}

			// If error is not expected, then check value matches.
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ExecuteNode(%T, %T); diff -want +got:\n%s", tc.node, tc.scope, diff)
			}
		})
	}
}

type mockInput struct {
	displayName string
	contents    string
}

func (m mockInput) DisplayName() string { return m.displayName }
func (m mockInput) Contents() []byte    { return []byte(m.contents) }
func (m mockInput) Equal(other syntax.InputSource) bool {
	return bytes.Equal(m.Contents(), other.Contents())
}

func TestExecFile(t *testing.T) {
	for _, file := range []string{
		"testdata/bool.gni",
		"testdata/int.gni",
		"testdata/functions.gni",
		"testdata/lists.gni",
		"testdata/resolve.gni",
		"testdata/scope.gni",
		"testdata/string.gni",
	} {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("failed to read %q: %v", file, err)
		}
		tokens, err := syntax.Tokenize(mockInput{
			displayName: file,
			contents:    string(content),
		})
		if err != nil {
			t.Fatalf("failed to tokenize %q: %v", file, err)
		}
		root, err := parse.Parse(tokens)
		if err != nil {
			t.Fatalf("failed to parse %q: %v", file, err)
		}

		_, err = ExecuteNode(root, &Scope{
			functions: map[string]FunctionInfo{
				"assert":         AssertFunction{},
				"assert_failure": assertFailureFunction{},
			},
			values: map[string]record{},
		})
		if err != nil {
			t.Errorf("execute failed: %v", err)
		}
	}
}
