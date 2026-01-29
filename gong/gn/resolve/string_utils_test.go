// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.
package resolve

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

func TestExpandStringLiteral(t *testing.T) {
	for _, tc := range []struct {
		name    string
		token   syntax.Token
		want    Value
		wantErr any
	}{
		{
			name:  "simple",
			token: syntax.MakeToken(syntax.TokenString, `"hello"`),
			want:  &StringValue{value: "hello"},
		},
		{
			name:  "empty",
			token: syntax.MakeToken(syntax.TokenString, `""`),
			want:  &StringValue{value: ""},
		},
		{
			name:  "escaped_quote",
			token: syntax.MakeToken(syntax.TokenString, `"\"hello\""`),
			want:  &StringValue{value: `"hello"`},
		},
		{
			name:  "escaped_backslash",
			token: syntax.MakeToken(syntax.TokenString, `"a\\b"`),
			want:  &StringValue{value: "a\\b"},
		},
		{
			name:  "escaped_dollar",
			token: syntax.MakeToken(syntax.TokenString, `"a\$b"`),
			want:  &StringValue{value: "a$b"},
		},
		{
			name:    "not_a_string_token",
			token:   syntax.MakeToken(syntax.TokenInteger, `123`),
			wantErr: &TypeError{},
		},
		{
			name:    "invalid_short_string",
			token:   syntax.MakeToken(syntax.TokenString, `"`),
			wantErr: &ASTError{},
		},
		{
			name:    "invalid_wrong_quote",
			token:   syntax.MakeToken(syntax.TokenString, `'foo'`),
			wantErr: &ASTError{},
		},
		{
			name:    "trailing_dollar",
			token:   syntax.MakeToken(syntax.TokenString, `"foo$"`),
			wantErr: &StringLiteralError{},
		},
		{
			name:  "hex_literal",
			token: syntax.MakeToken(syntax.TokenString, `"$0xFF"`),
			want:  &StringValue{value: "\xFF"},
		},
		{
			name:  "hex_literal_mixed",
			token: syntax.MakeToken(syntax.TokenString, `"$0x0AA"`),
			want:  &StringValue{value: "\x0AA"},
		},
		{
			name:  "hex_literal_multiple",
			token: syntax.MakeToken(syntax.TokenString, `"$0x0a$0xfF"`),
			want:  &StringValue{value: "\x0A\xFF"},
		},
		{
			name:    "hex_truncated_0",
			token:   syntax.MakeToken(syntax.TokenString, `"$0"`),
			wantErr: &StringLiteralError{},
		},
		{
			name:    "hex_truncated_0x",
			token:   syntax.MakeToken(syntax.TokenString, `"$0x"`),
			wantErr: &StringLiteralError{},
		},
		{
			name:    "hex_truncated_0x0",
			token:   syntax.MakeToken(syntax.TokenString, `"$0x0"`),
			wantErr: &StringLiteralError{},
		},
		{
			name:    "hex_bad_char_0a",
			token:   syntax.MakeToken(syntax.TokenString, `"$0a"`),
			wantErr: &StringLiteralError{},
		},
		{
			name:    "hex_bad_char_0x1z",
			token:   syntax.MakeToken(syntax.TokenString, `"$0x1z"`),
			wantErr: &StringLiteralError{},
		},
		{
			name:    "hex_bad_char_0xz1",
			token:   syntax.MakeToken(syntax.TokenString, `"$0xz1"`),
			wantErr: &StringLiteralError{},
		},
		{
			name:    "unimplemented_identifier",
			token:   syntax.MakeToken(syntax.TokenString, `"$foo"`),
			wantErr: &StringLiteralError{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandStringLiteral(tc.token, &parse.LiteralNode{
				Token: syntax.MakeToken(syntax.TokenString, "origin"),
			})
			wantErr := tc.wantErr != nil
			gotErr := err != nil

			if gotErr != wantErr {
				t.Fatalf("expandStringLiteral(%v): got err=%v, wantErr %T", tc.token, err, tc.wantErr)
			}

			if gotErr {
				if !errors.As(err, tc.wantErr) {
					t.Errorf("expandStringLiteral(%v) got err=%v (%T), wantErr %T", tc.token, err, err, tc.wantErr)
				}
				return
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("expandStringLiteral(%v); diff -want +got:\n%s", tc.token, diff)
			}
		})
	}
}
