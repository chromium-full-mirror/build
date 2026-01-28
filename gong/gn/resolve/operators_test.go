// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

func TestBinaryOps_NoSideEffects(t *testing.T) {
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
			name: "integers_addition",
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenPlus, "+"),
				Left:  &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "2")},
			},
			want:        &IntegerValue{value: 3},
			wantErrKind: syntax.ErrNone,
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
			got, err := executeBinaryOperator(tc.node, &Scope{values: map[string]record{}})
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

func TestBinaryOps_Assignment(t *testing.T) {
	for _, tc := range []struct {
		name        string
		scope       *Scope
		node        *parse.BinaryOpNode
		ident       string
		want        Value
		wantErrKind syntax.ErrKind
	}{
		{
			name:  "assign_integer",
			scope: &Scope{values: map[string]record{}},
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenEqual, "="),
				Left:  &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "a")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "123")},
			},
			ident:       "a",
			want:        &IntegerValue{value: 123},
			wantErrKind: syntax.ErrNone,
		},
		{
			name:  "assign_string",
			scope: &Scope{values: map[string]record{}},
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenEqual, "="),
				Left:  &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "a")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenString, `"b"`)},
			},
			ident:       "a",
			want:        &StringValue{value: "b"},
			wantErrKind: syntax.ErrNone,
		},
		{
			name:  "assign_list",
			scope: &Scope{values: map[string]record{}},
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenEqual, "="),
				Left:  &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "a")},
				Right: &parse.ListNode{Contents: []parse.Node{&parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")}}},
			},
			ident:       "a",
			want:        &ListValue{list: []Value{&IntegerValue{value: 1}}},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "assign_list_clobber",
			scope: &Scope{values: map[string]record{
				"a": {value: &ListValue{list: []Value{&IntegerValue{value: 1}}}},
			}},
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenEqual, "="),
				Left:  &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "a")},
				Right: &parse.ListNode{Contents: []parse.Node{&parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")}}},
			},
			wantErrKind: syntax.ErrInvalidOperation,
		},
		{
			name: "assign_accessor",
			scope: &Scope{values: map[string]record{
				"a": {value: &ScopeValue{scope: &Scope{values: map[string]record{}}}},
			}},
			node: &parse.BinaryOpNode{
				Op: syntax.MakeToken(syntax.TokenEqual, "="),
				Left: &parse.AccessorNode{
					Base:   syntax.MakeToken(syntax.TokenIdentifier, "a"),
					Member: &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "b")},
				},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "123")},
			},
			ident:       "a.b",
			want:        &IntegerValue{value: 123},
			wantErrKind: syntax.ErrNone,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := executeBinaryOperator(tc.node, tc.scope)
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
			// TODO: fix if destination is not simple ident
			if !strings.Contains(tc.ident, ".") {
				if diff := cmp.Diff(tc.want, tc.scope.values[tc.ident].value); diff != "" {
					t.Errorf("scope.values[%q].value; diff -want +got:\n%s", tc.ident, diff)
				}
			}
		})
	}
}

func TestBinaryOps_PlusEquals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		scope *Scope
		node  *parse.BinaryOpNode
		want  Value
		// TODO: this is a temporary hack to support errors being returned with
		// the deprecated ErrKind type versus strongly-typed errors.
		wantErrKind syntax.ErrKind
		wantErr     any
	}{
		{
			name: "int_plus_equals",
			scope: &Scope{values: map[string]record{
				"dest": {value: &IntegerValue{value: 1}},
			}},
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenPlusEquals, "+="),
				Left:  &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "dest")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "2")},
			},
			want:        &IntegerValue{value: 3},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "string_plus_equals",
			scope: &Scope{values: map[string]record{
				"dest": {value: &StringValue{value: "foo"}},
			}},
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenPlusEquals, "+="),
				Left:  &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "dest")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenString, `"bar"`)},
			},
			want:        &StringValue{value: "foobar"},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "string_plus_equals_int",
			scope: &Scope{values: map[string]record{
				"dest": {value: &StringValue{value: "foo"}},
			}},
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenPlusEquals, "+="),
				Left:  &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "dest")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
			},
			want:        &StringValue{value: "foo1"},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "list_plus_equals",
			scope: &Scope{values: map[string]record{
				"dest": {value: &ListValue{list: []Value{&IntegerValue{value: 1}}}},
			}},
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenPlusEquals, "+="),
				Left:  &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "dest")},
				Right: &parse.ListNode{Contents: []parse.Node{&parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "2")}}},
			},
			want:        &ListValue{list: []Value{&IntegerValue{value: 1}, &IntegerValue{value: 2}}},
			wantErrKind: syntax.ErrNone,
		},
		{
			name: "list_plus_equals_num_fail",
			scope: &Scope{values: map[string]record{
				"dest": {value: &ListValue{list: []Value{}}},
			}},
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenPlusEquals, "+="),
				Left:  &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "dest")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
			},
			wantErrKind: syntax.ErrTypeMismatch,
		},
		{
			name:  "undefined_variable",
			scope: &Scope{values: map[string]record{}},
			node: &parse.BinaryOpNode{
				Op:    syntax.MakeToken(syntax.TokenPlusEquals, "+="),
				Left:  &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "dest")},
				Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "1")},
			},
			wantErr: &UndefinedIdentifierError{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := executeBinaryOperator(tc.node, tc.scope)

			gotErr := err != nil
			wantErr := tc.wantErr != nil || tc.wantErrKind != syntax.ErrNone
			if gotErr != wantErr {
				t.Fatalf("executeBinaryOperator error mismatch: got %v, want kind %v", err, tc.wantErrKind)
			}
			if gotErr {
				// TODO: temporary hack to support both wantErr and wantErrKind tests.
				if tc.wantErr != nil {
					if !errors.As(err, tc.wantErr) {
						t.Errorf("executeBinaryOperator(_, _) got err=%v (%T), want %T", err, err, tc.wantErr)
					}
				} else {
					if match, _ := syntax.AsErrKind(err, tc.wantErrKind); match == nil {
						t.Errorf("error kind mismatch: got %v, want %v", err, tc.wantErrKind)
					}
				}
				return
			} else if got != nil {
				t.Errorf("executeBinaryOperator(_, _) = nil, _; got %v", got)
				return
			}
			scopeVal := tc.scope.Value("dest", false)
			if diff := cmp.Diff(tc.want, scopeVal); diff != "" {
				t.Errorf("dest value mismatch (-want +got):\n%s", diff)
			}
		})
	}

	t.Run("parent_scope_copy_on_write", func(t *testing.T) {
		parentValue := &IntegerValue{value: 15}
		parent := &Scope{values: map[string]record{
			"a": {value: parentValue},
		}}
		scope := parent.NewNestedScope()

		_, err := executeBinaryOperator(&parse.BinaryOpNode{
			Op:    syntax.MakeToken(syntax.TokenPlusEquals, "+="),
			Left:  &parse.IdentifierNode{Value: syntax.MakeToken(syntax.TokenIdentifier, "a")},
			Right: &parse.LiteralNode{Token: syntax.MakeToken(syntax.TokenInteger, "5")},
		}, scope)

		gotErr := err != nil
		if gotErr {
			t.Errorf("executeBinaryOperator(_, _) = _, %v; want nil error", err)
			return
		}
		if diff := cmp.Diff(&IntegerValue{value: 20}, scope.Value("a", false)); diff != "" {
			t.Errorf("scope value mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(parentValue, parent.Value("a", false)); diff != "" {
			t.Errorf("parent scope value mismatch (-want +got):\n%s", diff)
		}
	})
}
