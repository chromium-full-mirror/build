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
