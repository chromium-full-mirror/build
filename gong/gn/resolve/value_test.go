// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"math"
	"testing"
)

func TestStringification(t *testing.T) {
	for _, tc := range []struct {
		name             string
		input            Value
		expectedRaw      string
		expectedLiteral  string
		expectedStarlark string
	}{
		{
			name:             "boolean_false",
			input:            &BooleanValue{value: false},
			expectedRaw:      "false",
			expectedLiteral:  "false",
			expectedStarlark: "False",
		},
		{
			name:             "boolean_true",
			input:            &BooleanValue{value: true},
			expectedRaw:      "true",
			expectedLiteral:  "true",
			expectedStarlark: "True",
		},
		{
			name:             "integer",
			input:            &IntegerValue{value: math.MaxInt32 + 1},
			expectedRaw:      "2147483648",
			expectedLiteral:  "2147483648",
			expectedStarlark: "2147483648",
		},
		{
			name: "newlines",
			input: &StringValue{value: `foo
bar`},
			expectedRaw: `foo
bar`,
			expectedLiteral: `"foo
bar"`,
			// Starlark will quote special characters like newlines, unlike GN.
			expectedStarlark: `"foo\nbar"`,
		},
		{
			// Test copied from GN, don't use ` to quote string so that we can see it's identical.
			name:            "string",
			input:           &StringValue{value: "hi\" $me\\you\\$\\\""},
			expectedRaw:     "hi\" $me\\you\\$\\\"",
			expectedLiteral: "\"hi\\\" \\$me\\you\\\\\\$\\\\\\\"\"",
			// Starlark-quoted strings should not escape $ characters.
			expectedStarlark: `"hi\" $me\\you\\$\\\""`,
		},
		{
			// Test copied from GN, don't use ` to quote string so that we can see it's identical.
			name:            "crbug.com/470217",
			input:           &StringValue{value: "\\foo\\\\bar\\"},
			expectedRaw:     "\\foo\\\\bar\\",
			expectedLiteral: "\"\\foo\\\\\\bar\\\\\"",
			// Starlark string quoting behavior differs from GN here.
			expectedStarlark: "\"\\\\foo\\\\\\\\bar\\\\\"",
		},
		{
			name: "list",
			input: &ListValue{
				list: []Value{
					&StringValue{value: `hi"me`},
					&BooleanValue{value: true},
					&BooleanValue{value: false},
					&IntegerValue{value: 42},
				},
			},
			// Printing lists always causes embedded strings to be quoted (ignoring the
			// quote flag), or else they wouldn't make much sense.
			expectedRaw:      `["hi\"me", true, false, 42]`,
			expectedLiteral:  `["hi\"me", true, false, 42]`,
			expectedStarlark: `["hi\"me", True, False, 42]`,
		},
		{
			name: "scope_empty",
			input: &ScopeValue{
				scope: &Scope{
					values: map[string]record{},
				},
			},
			expectedRaw:      `{ }`,
			expectedLiteral:  `{ }`,
			expectedStarlark: `<gnscope>`,
		},
		{
			name: "scope_values",
			input: &ScopeValue{
				scope: &Scope{
					values: map[string]record{
						"a": {value: &IntegerValue{value: 42}},
						"b": {value: &StringValue{value: "hello, world"}},
					},
				},
			},
			expectedRaw: `{
  a = 42
  b = "hello, world"
}`,
			expectedLiteral: `{
  a = 42
  b = "hello, world"
}`,
			expectedStarlark: `<gnscope>`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.input.RawGNString()
			if got != tc.expectedRaw {
				t.Errorf("RawGNString() = %s, want %s", got, tc.expectedRaw)
			}

			got = GNLiteralRvalue(tc.input)
			if got != tc.expectedLiteral {
				t.Errorf("GNLiteralRvalue() = %s, want %s", got, tc.expectedLiteral)
			}

			got = tc.input.String()
			if got != tc.expectedStarlark {
				t.Errorf("String() = %s, want %s", got, tc.expectedStarlark)
			}
		})
	}
}

type fakeExecContext struct{}

func (fakeExecContext) BaseConfig() *Scope { return &Scope{} }

func TestEqual(t *testing.T) {
	for _, tc := range []struct {
		name  string
		left  Value
		right Value
		want  bool
	}{
		{
			name:  "bool_equal",
			left:  &BooleanValue{value: true},
			right: &BooleanValue{value: true},
			want:  true,
		},
		{
			name:  "bool_different",
			left:  &BooleanValue{value: true},
			right: &BooleanValue{value: false},
			want:  false,
		},
		{
			name:  "int_equal",
			left:  &IntegerValue{value: 1},
			right: &IntegerValue{value: 1},
			want:  true,
		},
		{
			name:  "int_different",
			left:  &IntegerValue{value: 1},
			right: &IntegerValue{value: 2},
			want:  false,
		},
		{
			name:  "string_equal",
			left:  &StringValue{value: "a"},
			right: &StringValue{value: "a"},
			want:  true,
		},
		{
			name:  "string_different",
			left:  &StringValue{value: "a"},
			right: &StringValue{value: "b"},
			want:  false,
		},
		{
			name: "list_equal",
			left: &ListValue{
				list: []Value{
					&StringValue{value: "a"},
					&IntegerValue{value: 1},
				},
			},
			right: &ListValue{
				list: []Value{
					&StringValue{value: "a"},
					&IntegerValue{value: 1},
				},
			},
			want: true,
		},
		{
			name: "list_different_values",
			left: &ListValue{
				list: []Value{
					&StringValue{value: "a"},
					&IntegerValue{value: 1},
				},
			},
			right: &ListValue{
				list: []Value{
					&StringValue{value: "a"},
					&IntegerValue{value: 2},
				},
			},
			want: false,
		},
		{
			name: "list_different_length",
			left: &ListValue{
				list: []Value{
					&StringValue{value: "a"},
					&IntegerValue{value: 1},
				},
			},
			right: &ListValue{
				list: []Value{
					&StringValue{value: "a"},
				},
			},
			want: false,
		},
		{
			name: "scope_equal",
			left: &ScopeValue{
				scope: &Scope{
					values: map[string]record{
						"a": {value: &IntegerValue{value: 1}},
						"b": {value: &StringValue{value: "a"}},
					},
				},
			},
			right: &ScopeValue{
				scope: &Scope{
					values: map[string]record{
						"a": {value: &IntegerValue{value: 1}},
						"b": {value: &StringValue{value: "a"}},
					},
				},
			},
			want: true,
		},
		{
			name: "scope_left_has_parent",
			left: &ScopeValue{
				scope: &Scope{
					parent: &Scope{},
					values: map[string]record{"a": {value: &IntegerValue{value: 1}}},
				},
			},
			right: &ScopeValue{
				scope: &Scope{
					values: map[string]record{"a": {value: &IntegerValue{value: 1}}},
				},
			},
			want: false,
		},
		{
			name: "scope_left_has_exec_context",
			left: &ScopeValue{
				scope: &Scope{
					execContext: &fakeExecContext{},
					values:      map[string]record{"a": {value: &IntegerValue{value: 1}}},
				},
			},
			right: &ScopeValue{
				scope: &Scope{
					values: map[string]record{"a": {value: &IntegerValue{value: 1}}},
				},
			},
			want: false,
		},
		{
			name: "scope_isolated_equal",
			left: &ScopeValue{
				scope: &Scope{
					execContext:    &fakeExecContext{},
					skipBaseConfig: true,
					values: map[string]record{
						"a": {value: &IntegerValue{value: 1}},
						"b": {value: &StringValue{value: "a"}},
					},
				},
			},
			right: &ScopeValue{
				scope: &Scope{
					values: map[string]record{
						"a": {value: &IntegerValue{value: 1}},
						"b": {value: &StringValue{value: "a"}},
					},
				},
			},
			want: true,
		},
		{
			name: "scope_different_values",
			left: &ScopeValue{
				scope: &Scope{
					values: map[string]record{
						"a": {value: &IntegerValue{value: 1}},
						"b": {value: &StringValue{value: "a"}},
					},
				},
			},
			right: &ScopeValue{
				scope: &Scope{
					values: map[string]record{
						"a": {value: &IntegerValue{value: 1}},
						"b": {value: &StringValue{value: "b"}},
					},
				},
			},
			want: false,
		},
		{
			name: "scope_missing_values",
			left: &ScopeValue{
				scope: &Scope{
					values: map[string]record{
						"a": {value: &IntegerValue{value: 1}},
						"b": {value: &StringValue{value: "a"}},
					},
				},
			},
			right: &ScopeValue{
				scope: &Scope{
					values: map[string]record{
						"a": {value: &IntegerValue{value: 1}},
					},
				},
			},
			want: false,
		},
		{
			name:  "different_types",
			left:  &StringValue{value: "a"},
			right: &IntegerValue{value: 1},
			want:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.left.Equal(tc.right); got != tc.want {
				t.Errorf("left.Equal(right) = %v, want %v", got, tc.want)
			}
		})
	}
}
