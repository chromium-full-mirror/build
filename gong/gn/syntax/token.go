// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package syntax implements GN syntax tokenizer and parser.
package syntax

type TokenType int

const (
	// TokenInvalid represents an invalid token.
	TokenInvalid TokenType = iota
	// TokenInteger represents an integer literal.
	TokenInteger
	// TokenString represents a string literal "blah", including quotes.
	TokenString
	// TokenTrue represents a true boolean literal.
	TokenTrue
	// TokenFalse represents a false boolean literal.
	TokenFalse

	// TokenEqual represents the assignment operator "=".
	TokenEqual
	// TokenPlus represents the addition operator "+".
	TokenPlus
	// TokenMinus represents the subtraction operator "-".
	TokenMinus
	// TokenPlusEquals represents the add-and-assign operator "+=".
	TokenPlusEquals
	// TokenMinusEquals represents the subtract-and-assign operator "-=".
	TokenMinusEquals
	// TokenEqualEqual represents the equality operator "==".
	TokenEqualEqual
	// TokenNotEqual represents the inequality operator "!=".
	TokenNotEqual
	// TokenLessEqual represents the less than or equal to operator "<=".
	TokenLessEqual
	// TokenGreaterEqual represents the greater than or equal to operator ">=".
	TokenGreaterEqual
	// TokenLessThan represents the less than operator "<".
	TokenLessThan
	// TokenGreaterThan represents the greater than operator ">".
	TokenGreaterThan
	// TokenBooleanAnd represents the logical AND operator "&&".
	TokenBooleanAnd
	// TokenBooleanOr represents the logical OR operator "||".
	TokenBooleanOr
	// TokenBang represents the logical NOT operator "!".
	TokenBang
	// TokenDot represents the member access operator ".".
	TokenDot

	// TokenLeftParen represents a left parenthesis "(".
	TokenLeftParen
	// TokenRightParen represents a right parenthesis ")".
	TokenRightParen
	// TokenLeftBracket represents a left bracket "[".
	TokenLeftBracket
	// TokenRightBracket represents a right bracket "]".
	TokenRightBracket
	// TokenLeftBrace represents a left brace "{".
	TokenLeftBrace
	// TokenRightBrace represents a right brace "}".
	TokenRightBrace

	// TokenIf represents the "if" keyword.
	TokenIf
	// TokenElse represents the "else" keyword.
	TokenElse
	// TokenIdentifier represents an identifier.
	TokenIdentifier
	// TokenComma represents a comma ",".
	TokenComma
	// TokenUnclassifiedComment represents a comment #...\n, of unknown style (will be converted to one below)
	TokenUnclassifiedComment
	// TokenLineComment represents a comment #...\n on a line alone.
	TokenLineComment
	// TokenSuffixComment represents a comment #...\n on a line following other code.
	TokenSuffixComment
	// TokenBlockComment represents a comment #...\n line comment, but free-standing.
	TokenBlockComment

	// TokenUnclassifiedOperator represents an operator that hasn't been classified yet.
	TokenUnclassifiedOperator
)

// Token represents a token in a GN file.
type Token struct {
	tokenType TokenType
	// TODO: consider just {file, start_offset, end_offset}
	// (and compute line/col when needed) to reduce memory usage?
	value    string
	location Location
}

type fakeInputSource struct {
	displayName string
	contents    string
}

func (m fakeInputSource) DisplayName() string          { return m.displayName }
func (m fakeInputSource) Contents() []byte             { return []byte(m.contents) }
func (m fakeInputSource) Equal(other InputSource) bool { return m == other }

// MakeToken returns a token for testing purposes.
// TODO: this should not be necessary for testing if it's possible to serialize an AST.
func MakeToken(tokenType TokenType, value string) Token {
	return Token{
		tokenType: tokenType,
		value:     value,
		location: Location{
			file: fakeInputSource{
				displayName: "<input>",
				contents:    value,
			},
		},
	}
}

// Range returns location range for this token.
func (t Token) Range() LocationRange {
	return LocationRange{
		begin: t.location,
		end: Location{
			file:         t.location.file,
			lineNumber:   t.location.lineNumber,
			columnNumber: t.location.columnNumber + len(t.value),
		},
	}
}

// TokenType returns type of this token.
func (t Token) TokenType() TokenType {
	return t.tokenType
}

// Value returns value of this token.
func (t Token) Value() string {
	// TODO: consider just {file, start_offset, end_offset}
	// (and compute line/col when needed) so this is lazy calculated?
	return t.value
}

// MakeError makes an error from this token and message.
//
// Deprecated: Implement ui.PresentableError instead.
func (t Token) MakeError(kind ErrKind, message string) error {
	return Error{
		location: t.location,
		ranges:   []LocationRange{t.Range()},
		message:  message,
		kind:     kind,
	}
}

// MakeErrorWithHelp makes an error from this token and message and help.
//
// Deprecated: Implement ui.PresentableError instead.
func (t Token) MakeErrorWithHelp(kind ErrKind, message, helpText string) error {
	return Error{
		location: t.location,
		ranges:   []LocationRange{t.Range()},
		message:  message,
		helpText: helpText,
		kind:     kind,
	}
}
