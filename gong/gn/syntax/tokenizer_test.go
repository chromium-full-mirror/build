// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package syntax

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

type mockInput struct {
	displayName string
	contents    string
}

func (m mockInput) DisplayName() string          { return m.displayName }
func (m mockInput) Contents() []byte             { return []byte(m.contents) }
func (m mockInput) Equal(other InputSource) bool { return false }

func TestTokenizer(t *testing.T) {
	cmpOpts := []cmp.Option{
		cmp.AllowUnexported(Token{}),
		cmpopts.IgnoreFields(Token{}, "location"),
	}

	for _, tc := range []struct {
		input    string
		expected []Token
	}{
		{
			input:    "",
			expected: []Token{},
		},
		{
			input:    "  \r \n \r\n",
			expected: []Token{},
		},
		{
			input: "  foo ",
			expected: []Token{
				{tokenType: TokenIdentifier, value: "foo"},
			},
		},
		{
			input: "  123 -123 ",
			expected: []Token{
				{tokenType: TokenInteger, value: "123"},
				{tokenType: TokenInteger, value: "-123"},
			},
		},
		{
			input: "  123-123 ",
			expected: []Token{
				{tokenType: TokenInteger, value: "123"},
				{tokenType: TokenInteger, value: "-123"},
			},
		},
		{
			input: `  "foo" "bar\"baz" "asdf\\" `,
			expected: []Token{
				{tokenType: TokenString, value: `"foo"`},
				{tokenType: TokenString, value: `"bar\"baz"`},
				{tokenType: TokenString, value: `"asdf\\"`}},
		},
		{
			input: "- + = += -= != ==  < > <= >= ! || && . ,",
			expected: []Token{
				{tokenType: TokenMinus, value: "-"},
				{tokenType: TokenPlus, value: "+"},
				{tokenType: TokenEqual, value: "="},
				{tokenType: TokenPlusEquals, value: "+="},
				{tokenType: TokenMinusEquals, value: "-="},
				{tokenType: TokenNotEqual, value: "!="},
				{tokenType: TokenEqualEqual, value: "=="},
				{tokenType: TokenLessThan, value: "<"},
				{tokenType: TokenGreaterThan, value: ">"},
				{tokenType: TokenLessEqual, value: "<="},
				{tokenType: TokenGreaterEqual, value: ">="},
				{tokenType: TokenBang, value: "!"},
				{tokenType: TokenBooleanOr, value: "||"},
				{tokenType: TokenBooleanAnd, value: "&&"},
				{tokenType: TokenDot, value: "."},
				{tokenType: TokenComma, value: ","},
			},
		},
		{
			input: "{[ ]} ()",
			expected: []Token{
				{tokenType: TokenLeftBrace, value: "{"},
				{tokenType: TokenLeftBracket, value: "["},
				{tokenType: TokenRightBracket, value: "]"},
				{tokenType: TokenRightBrace, value: "}"},
				{tokenType: TokenLeftParen, value: "("},
				{tokenType: TokenRightParen, value: ")"},
			},
		},
		{
			input: `fun("foo") {
foo = 12}`,
			expected: []Token{
				{tokenType: TokenIdentifier, value: "fun"},
				{tokenType: TokenLeftParen, value: "("},
				{tokenType: TokenString, value: `"foo"`},
				{tokenType: TokenRightParen, value: ")"},
				{tokenType: TokenLeftBrace, value: "{"},
				{tokenType: TokenIdentifier, value: "foo"},
				{tokenType: TokenEqual, value: "="},
				{tokenType: TokenInteger, value: "12"},
				{tokenType: TokenRightBrace, value: "}"},
			},
		},
		{
			input: `# Stuff
fun("foo") {  # Things
#Wee
foo = 12 #Zip
}`,
			expected: []Token{
				{tokenType: TokenLineComment, value: "# Stuff"},
				{tokenType: TokenIdentifier, value: "fun"},
				{tokenType: TokenLeftParen, value: "("},
				{tokenType: TokenString, value: `"foo"`},
				{tokenType: TokenRightParen, value: ")"},
				{tokenType: TokenLeftBrace, value: "{"},
				{tokenType: TokenSuffixComment, value: "# Things"},
				{tokenType: TokenLineComment, value: "#Wee"},
				{tokenType: TokenIdentifier, value: "foo"},
				{tokenType: TokenEqual, value: "="},
				{tokenType: TokenInteger, value: "12"},
				{tokenType: TokenSuffixComment, value: "#Zip"},
				{tokenType: TokenRightBrace, value: "}"},
			},
		},
		{
			// In this test, the comments aren't horizontally aligned, so they're
			// considered separate.
			input: `
fun("foo") {  # A
  # B
}`,
			expected: []Token{
				{tokenType: TokenIdentifier, value: "fun"},
				{tokenType: TokenLeftParen, value: "("},
				{tokenType: TokenString, value: `"foo"`},
				{tokenType: TokenRightParen, value: ")"},
				{tokenType: TokenLeftBrace, value: "{"},
				{tokenType: TokenSuffixComment, value: "# A"},
				{tokenType: TokenLineComment, value: "# B"},
				{tokenType: TokenRightBrace, value: "}"},
			},
		},
		{
			// In this test, they are, so "B" is a continuation of "A" (another
			// TokenSuffixComment).
			input: `
fun("foo") {  # A
              # B
}`,
			expected: []Token{
				{tokenType: TokenIdentifier, value: "fun"},
				{tokenType: TokenLeftParen, value: "("},
				{tokenType: TokenString, value: `"foo"`},
				{tokenType: TokenRightParen, value: ")"},
				{tokenType: TokenLeftBrace, value: "{"},
				{tokenType: TokenSuffixComment, value: "# A"},
				{tokenType: TokenSuffixComment, value: "# B"},
				{tokenType: TokenRightBrace, value: "}"},
			},
		},
	} {
		got, err := Tokenize(mockInput{
			displayName: "test.gni",
			contents:    tc.input,
		})
		if err != nil {
			t.Errorf("Tokenize(%q) = _, %v; want nil error", tc.input, err)
			continue
		}

		if diff := cmp.Diff(tc.expected, got, cmpOpts...); diff != "" {
			t.Errorf("Tokenize(%q); diff -want +got:\n%s", tc.input, diff)
		}
	}
}
func TestTokenizerWhitespace(t *testing.T) {
	input := mockInput{
		displayName: "test.gni",
		contents:    "a\t2\v\"st\tuff\"\f{",
	}

	_, err := TokenizeWithTransform(input, whitespaceTransformMaintainOriginalInput)
	if err == nil {
		t.Errorf("TokenizeWithTransform(_, whitespaceTransformMaintainOriginalInput) = _, nil; want error")
	}

	got, err := TokenizeWithTransform(input, whitespaceTransformInvalidToSpace)
	if err != nil {
		t.Errorf("TokenizeWithTransform(_, whitespaceTransformInvalidToSpace) = _, %v; want nil error", err)
		return
	}

	want := []Token{
		{tokenType: TokenIdentifier, value: "a"},
		{tokenType: TokenInteger, value: "2"},
		{tokenType: TokenString, value: "\"st\tuff\""}, // Internal tab should remain.
		{tokenType: TokenLeftBrace, value: "{"},
	}

	cmpOpts := []cmp.Option{
		cmp.AllowUnexported(Token{}),
		cmpopts.IgnoreFields(Token{}, "location"),
	}
	if diff := cmp.Diff(want, got, cmpOpts...); diff != "" {
		t.Errorf("TokenizeWithTransform(_, whitespaceTransformInvalidToSpace); diff -want +got:\n%s", diff)
	}
}
