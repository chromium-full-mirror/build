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

func TestBinaryOps(t *testing.T) {
	for _, tc := range []struct {
		name        string
		node        *parse.BinaryOpNode
		want        Value
		wantErrKind syntax.ErrKind
	}{
		{
			name: "integers_equal",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenEqualEqual, "=="),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
			},
			want:        &BooleanValue{value: true},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "integers_not_equal",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenEqualEqual, "=="),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "2")},
			},
			want:        &BooleanValue{value: false},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "integers_not_equal_operator",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenNotEqual, "!="),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "2")},
			},
			want:        &BooleanValue{value: true},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "integers_greater_than",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenGreaterThan, ">"),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "2")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
			},
			want:        &BooleanValue{value: true},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "integers_greater_than_or_equal",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenGreaterEqual, ">="),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "2")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "2")},
			},
			want:        &BooleanValue{value: true},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "integers_less_than",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenLessThan, "<"),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "2")},
			},
			want:        &BooleanValue{value: true},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "integers_less_than_or_equal",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenLessEqual, "<="),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "2")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "2")},
			},
			want:        &BooleanValue{value: true},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "type_mismatch_comparison",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenLessThan, "<"),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenString, `"2"`)},
			},
			wantErrKind: syntax.ErrTypeMismatch,
		},
		{
			name: "not_implemented_plus",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenPlus, "+"),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "2")},
			},
			wantErrKind: syntax.ErrNotImplemented,
		},
		{
			name: "or_true_true",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenBooleanOr, "||"),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenTrue, "true")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenTrue, "true")},
			},
			want:        &BooleanValue{value: true},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "or_true_false",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenBooleanOr, "||"),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenTrue, "true")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenFalse, "false")},
			},
			want:        &BooleanValue{value: true},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "or_false_true",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenBooleanOr, "||"),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenFalse, "false")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenTrue, "true")},
			},
			want:        &BooleanValue{value: true},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "or_false_false",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenBooleanOr, "||"),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenFalse, "false")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenFalse, "false")},
			},
			want:        &BooleanValue{value: false},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "or_short_circuit",
			node: &parse.BinaryOpNode{
				Op:   syntax.MakeToken(syntax.TokenBooleanOr, "||"),
				Left: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenTrue, "true")},
				// GN's eager execution of || means the non-boolean RHS is never seen.
				Right: &parse.BinaryOpNode{
					Op:    syntax.MakeToken(syntax.TokenPlus, "+"),
					Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
					Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
				},
			},
			want:        &BooleanValue{value: true},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "or_type_mismatch_left",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenBooleanOr, "||"),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenTrue, "true")},
			},
			wantErrKind: syntax.ErrTypeMismatch,
		},
		{
			name: "or_type_mismatch_right",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenBooleanOr, "||"),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenFalse, "false")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
			},
			wantErrKind: syntax.ErrTypeMismatch,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := executeBinaryOperator(tc.node, &Scope{})
			wantErr := tc.wantErrKind != syntax.ErrNone
			gotErr := err != nil

			if gotErr != wantErr {
				t.Fatalf("executeBinaryOperator(%T, _): got err=%v, wantErrKind=%v", tc.node, err, tc.wantErrKind)
			}

			if gotErr {
				if match, gotErrKind := syntax.AsErrKind(err, tc.wantErrKind); match == nil {
					t.Fatalf("executeBinaryOperator(%T, _): got err=%v (kind %s), wantErrKind=%s", tc.node, err, gotErrKind, tc.wantErrKind)
				}
				return
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("executeBinaryOperator(%T, _); diff -want +got:\n%s", tc.node, diff)
			}
		})
	}
}
