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
		name    string
		node    parse.ParseNode
		scope   *Scope
		want    Value
		wantErr bool
	}{
		{
			name:    "blockcomment_nothing",
			node:    &parse.BlockCommentNode{},
			scope:   &Scope{},
			wantErr: false,
		},
		{
			name:    "literal_true",
			node:    &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenTrue, "true")},
			scope:   &Scope{},
			want:    &BooleanValue{value: true},
			wantErr: false,
		},
		{
			name:    "literal_false",
			node:    &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenFalse, "false")},
			scope:   &Scope{},
			want:    &BooleanValue{value: false},
			wantErr: false,
		},
		{
			name:    "literal_integer",
			node:    &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "123")},
			scope:   &Scope{},
			want:    &IntegerValue{value: 123},
			wantErr: false,
		},
		{
			name:    "literal_integer_negative",
			node:    &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "-1")},
			scope:   &Scope{},
			want:    &IntegerValue{value: -1},
			wantErr: false,
		},
		{
			name:    "literal_integer_negative_zero",
			node:    &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "-0")},
			scope:   &Scope{},
			wantErr: true,
		},
		{
			name:    "literal_integer_leading_zeroes",
			node:    &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "01")},
			scope:   &Scope{},
			wantErr: true,
		},
		{
			name:    "literal_integer_negative_leading_zeroes",
			node:    &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "-01")},
			scope:   &Scope{},
			wantErr: true,
		},
		{
			name:    "literal_integer_invalid",
			node:    &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "123123612836217863781263781263786128371278637821678362817")},
			scope:   &Scope{},
			wantErr: true,
		},
		{
			name:    "literal_string",
			node:    &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenString, `"hello"`)},
			scope:   &Scope{},
			want:    &StringValue{value: "hello"},
			wantErr: false,
		},
		{
			name:    "literal_string_empty",
			node:    &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenString, `""`)},
			scope:   &Scope{},
			want:    &StringValue{value: ""},
			wantErr: false,
		},
		{
			name:    "literal_unhandled_token",
			node:    &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenPlus, "+")},
			scope:   &Scope{},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExecuteNode(tc.node, tc.scope)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("ExecuteNode(%T, %T) = %q, %v; want %q; err=%v", tc.node, tc.scope, got, err, tc.want, tc.wantErr)
				return
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ExecuteNode(%T, %T); diff -want +got:\n%s", tc.node, tc.scope, diff)
			}
		})
	}
}
