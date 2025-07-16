// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

func TestExecuteNode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		node        parse.ParseNode
		scope       *Scope
		want        Value
		wantErrKind syntax.ErrKind
	}{
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
			name:        "literal_unhandled_token",
			node:        &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenPlus, "+")},
			scope:       &Scope{},
			wantErrKind: syntax.ErrUnknown,
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
					Statements: []parse.ParseNode{},
				},
				IfFalse: &parse.BlockNode{
					Statements: []parse.ParseNode{},
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
					Statements: []parse.ParseNode{},
				},
				IfFalse: &parse.BlockNode{
					Statements: []parse.ParseNode{},
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
					Statements: []parse.ParseNode{},
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
					Statements: []parse.ParseNode{},
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
					Statements: []parse.ParseNode{},
				},
			},
			scope:       &Scope{},
			wantErrKind: syntax.ErrTypeMismatch,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExecuteNode(tc.node, tc.scope)
			wantErr := tc.wantErrKind != syntax.ErrNone
			gotErr := err != nil

			if gotErr != wantErr {
				t.Fatalf("ExecuteNode(%T, %T): got err=%v, wantErrKind=%v", tc.node, tc.scope, err, tc.wantErrKind)
			}

			// If error is expected, then check kind matches.
			if gotErr {
				if match, gotErrKind := syntax.AsErrKind(err, tc.wantErrKind); match == nil {
					t.Fatalf("ExecuteNode(%T, %T): got err=%v (kind %s), wantErrKind=%s", tc.node, tc.scope, err, gotErrKind, tc.wantErrKind)
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
