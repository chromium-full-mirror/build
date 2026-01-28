// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.
package resolve

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/syntax"
)

func TestExpandStringLiteral(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token syntax.Token
		want  Value
		// TODO: this is a temporary hack to support errors being returned with
		// the deprecated ErrKind type versus strongly-typed errors.
		wantErrKind syntax.ErrKind
		wantErr     any
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
			name:        "not_a_string_token",
			token:       syntax.MakeToken(syntax.TokenInteger, `123`),
			wantErrKind: syntax.ErrInvalidOperation,
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
			name:        "trailing_dollar",
			token:       syntax.MakeToken(syntax.TokenString, `"foo$"`),
			wantErrKind: syntax.ErrInvalidFormat,
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
			name:        "hex_truncated_0",
			token:       syntax.MakeToken(syntax.TokenString, `"$0"`),
			wantErrKind: syntax.ErrInvalidFormat,
		},
		{
			name:        "hex_truncated_0x",
			token:       syntax.MakeToken(syntax.TokenString, `"$0x"`),
			wantErrKind: syntax.ErrInvalidFormat,
		},
		{
			name:        "hex_truncated_0x0",
			token:       syntax.MakeToken(syntax.TokenString, `"$0x0"`),
			wantErrKind: syntax.ErrInvalidFormat,
		},
		{
			name:        "hex_bad_char_0a",
			token:       syntax.MakeToken(syntax.TokenString, `"$0a"`),
			wantErrKind: syntax.ErrInvalidFormat,
		},
		{
			name:        "hex_bad_char_0x1z",
			token:       syntax.MakeToken(syntax.TokenString, `"$0x1z"`),
			wantErrKind: syntax.ErrInvalidFormat,
		},
		{
			name:        "hex_bad_char_0xz1",
			token:       syntax.MakeToken(syntax.TokenString, `"$0xz1"`),
			wantErrKind: syntax.ErrInvalidFormat,
		},
		{
			name:        "unimplemented_identifier",
			token:       syntax.MakeToken(syntax.TokenString, `"$foo"`),
			wantErrKind: syntax.ErrNotImplemented,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandStringLiteral(tc.token, nil)
			wantErr := tc.wantErr != nil || tc.wantErrKind != ""
			gotErr := err != nil

			if gotErr != wantErr {
				t.Fatalf("expandStringLiteral(%v): got err=%v, wantErrKind=%v", tc.token, err, tc.wantErrKind)
			}

			if gotErr {
				// TODO: temporary hack to support both wantErr and wantErrKind tests.
				if tc.wantErr != nil {
					if !errors.As(err, tc.wantErr) {
						t.Errorf("expandStringLiteral(%v) got err=%v (%T), want %T", tc.token, err, err, tc.wantErr)
					}
				} else {
					if match, gotErrKind := syntax.AsErrKind(err, tc.wantErrKind); match == nil {
						t.Errorf("expandStringLiteral(%v): got err=%v (kind %s), wantErrKind=%s", tc.token, err, gotErrKind, tc.wantErrKind)
					}
				}
				return
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("expandStringLiteral(%v); diff -want +got:\n%s", tc.token, diff)
			}
		})
	}
}
