// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package syntax

import (
	"fmt"

	"infra/build/gong/gn/fs"
)

// whitespaceTransform is option for tokenization whitespace handling.
type whitespaceTransform int

// Tab (0x09), vertical tab (0x0B), and formfeed (0x0C) are illegal in GN files.
// Almost always these are errors. However, in the case of running the formatter
// it's nice to convert these to spaces when encountered so that the input can
// still be parsed and rewritten correctly by the formatter.
const (
	// whitespaceTransformMaintainOriginalInput maintains original input.
	whitespaceTransformMaintainOriginalInput whitespaceTransform = iota
	// whitespaceTransformInvalidToSpace transforms illegal whitespaces to spaces.
	whitespaceTransformInvalidToSpace
)

type tokenizer struct {
	tokens              []Token
	inputFile           *fs.InputFile
	input               string
	whitespaceTransform whitespaceTransform
	cur                 int // Byte offset into input buffer.
	lineNumber          int
	columnNumber        int
}

// Tokenize reads a GN input file and returns a list of tokens.
func Tokenize(inputFile *fs.InputFile) ([]Token, error) {
	return TokenizeWithTransform(inputFile, whitespaceTransformMaintainOriginalInput)
}

// TokenizeWithTransform reads a GN input file with provided whitespace transformation option and returns a list of tokens.
func TokenizeWithTransform(inputFile *fs.InputFile, whitespaceTransform whitespaceTransform) ([]Token, error) {
	tokenizer := tokenizer{
		inputFile:           inputFile,
		input:               inputFile.Contents,
		whitespaceTransform: whitespaceTransform,
		lineNumber:          1,
		columnNumber:        1,
	}
	return tokenizer.run()
}

func (s *tokenizer) run() ([]Token, error) {
	for !s.done() {
		s.advanceToNextToken()
		if s.done() {
			break
		}
		location := s.getCurrentLocation()

		tokenType := s.classifyCurrent()
		if tokenType == TokenInvalid {
			return nil, s.getErrorForInvalidToken(location)
		}

		// TODO: extract token value

		s.tokens = append(s.tokens, Token{
			location:  location,
			tokenType: tokenType,
		})
	}
	return nil, nil
}

func isNewline(input string, offset int) bool {
	// We may need more logic here to handle different line ending styles.
	return input[offset] == '\n'
}

func (s *tokenizer) advanceToNextToken() {
	for !s.atEnd() && s.isCurrentWhitespace() {
		s.advance()
	}
}

func classifyToken(nextChar, followingChar byte) TokenType {
	// TODO: implement
	return TokenInvalid
}

func (s *tokenizer) classifyCurrent() TokenType {
	nextChar := s.curChar()
	var followingChar byte
	if s.canIncrement() {
		followingChar = s.input[s.cur+1]
	}
	return classifyToken(nextChar, followingChar)
}

func (s *tokenizer) isCurrentWhitespace() bool {
	c := s.input[s.cur]
	// Note that tab (0x09), vertical tab (0x0B), and formfeed (0x0C) are illegal.
	return c == 0x0A || c == 0x0D || c == 0x20 ||
		(s.whitespaceTransform == whitespaceTransformInvalidToSpace &&
			(c == 0x09 || c == 0x0B || c == 0x0C))
}

func (s *tokenizer) isCurrentNewline() bool {
	return isNewline(s.input, s.cur)
}

func (s *tokenizer) canIncrement() bool {
	return s.cur < len(s.input)-1
}

func (s *tokenizer) advance() {
	if s.isCurrentNewline() {
		s.lineNumber++
		s.columnNumber = 1
	} else {
		s.columnNumber++
	}
	s.cur++
}

func (s *tokenizer) getCurrentLocation() Location {
	return Location{
		s.inputFile,
		s.lineNumber,
		s.columnNumber,
	}
}

func (s *tokenizer) getErrorForInvalidToken(location Location) error {
	help := "I have no idea what this is."
	switch s.curChar() {
	case ';':
		// Semicolon.
		help = "Semicolons are not needed, delete this one."
	case '\t':
		// Tab.
		help = "You got a tab character in here. Tabs are evil. Convert to spaces."
	case '\'':
		help = "Strings are delimited by \" characters, not apostrophes."
	case '/':
		if s.cur+1 < len(s.input) && (s.input[s.cur+1] == '/' || s.input[s.cur+1] == '*') {
			// Different types of comments.
			help = "Comments should start with # instead"
		}
	}

	// TODO: GN error struct?
	return fmt.Errorf("invalid token at %s:%d:%d: %s", location.file.Name.Filename(), location.lineNumber, location.columnNumber, help)
}

func (s *tokenizer) done() bool {
	return s.atEnd()
}

func (s *tokenizer) atEnd() bool {
	return s.cur == len(s.input)
}

func (s *tokenizer) curChar() byte {
	return s.input[s.cur]
}
