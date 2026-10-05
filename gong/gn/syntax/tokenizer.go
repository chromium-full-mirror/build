// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package syntax

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
	inputSource         InputSource
	input               []byte
	whitespaceTransform whitespaceTransform
	cur                 int // Byte offset into input buffer.
	lineNumber          int
	columnNumber        int
}

// Tokenize reads an input source and returns a list of tokens.
func Tokenize(inputSource InputSource) ([]Token, error) {
	return TokenizeWithTransform(inputSource, whitespaceTransformMaintainOriginalInput)
}

// TokenizeWithTransform reads an input source with provided whitespace transformation option and returns a list of tokens.
func TokenizeWithTransform(inputSource InputSource, whitespaceTransform whitespaceTransform) ([]Token, error) {
	tokenizer := tokenizer{
		inputSource:         inputSource,
		input:               inputSource.Contents(),
		whitespaceTransform: whitespaceTransform,
		tokens:              []Token{},
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

		// Determine broad category of token first.
		// Narrow it down afterwards.
		tokenType := s.classifyCurrent()
		if tokenType == TokenInvalid {
			return s.tokens, s.getErrorForInvalidToken(location)
		}

		// Extract token.
		tokenBegin := s.cur
		if err := s.advanceToEndOfToken(location, tokenType); err != nil {
			return s.tokens, err
		}
		tokenEnd := s.cur
		tokenValue := string(s.input[tokenBegin:tokenEnd])

		switch tokenType {
		case TokenUnclassifiedOperator:
			tokenType = getSpecificOperatorType(tokenValue)
		case TokenIdentifier:
			switch tokenValue {
			case "if":
				tokenType = TokenIf
			case "else":
				tokenType = TokenElse
			case "true":
				tokenType = TokenTrue
			case "false":
				tokenType = TokenFalse
			}
		case TokenUnclassifiedComment:
			// If it's a comment following other code, it's a suffix comment.
			if !s.atStartOfLine(tokenBegin) ||
				(len(s.tokens) != 0 &&
					// However if it's a standalone comment, that continues a comment on
					// a previous line, then it should also be a continued suffix comment.
					s.tokens[len(s.tokens)-1].tokenType == TokenSuffixComment &&
					s.tokens[len(s.tokens)-1].location.lineNumber+1 == location.lineNumber &&
					s.tokens[len(s.tokens)-1].location.columnNumber == location.columnNumber) {
				tokenType = TokenSuffixComment
			} else {
				tokenType = TokenLineComment
				// Could be EOF.
				if !s.atEnd() {
					s.advance() // The current \n.
				}
				// If this comment is separated from the next syntax element, then we
				// want to tag it as a block comment. This will become a standalone
				// statement at the parser level to keep this comment separate, rather
				// than attached to the subsequent statement.
				for !s.atEnd() && s.isCurrentWhitespace() {
					if isNewline(s.input, s.cur) {
						tokenType = TokenBlockComment
						break
					}
					s.advance()
				}
			}
		}

		s.tokens = append(s.tokens, Token{
			location:  location,
			tokenType: tokenType,
			value:     tokenValue,
		})
	}
	return s.tokens, nil
}

func isNewline(input []byte, offset int) bool {
	// We may need more logic here to handle different line ending styles.
	return input[offset] == '\n'
}

func isIdentifierFirstChar(c byte) bool {
	return isASCIIAlpha(c) || c == '_'
}

func isIdentifierContinuingChar(c byte) bool {
	// Also allow digits after the first char.
	return isIdentifierFirstChar(c) || isASCIIDigit(c)
}

func isASCIIDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func isASCIIAlpha(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func couldBeTwoCharOperatorBegin(c byte) bool {
	return c == '<' || c == '>' || c == '!' || c == '=' || c == '-' || c == '+' || c == '|' || c == '&'
}

func couldBeTwoCharOperatorEnd(c byte) bool {
	return c == '=' || c == '|' || c == '&'
}

func couldBeOneCharOperator(c byte) bool {
	return c == '=' || c == '<' || c == '>' || c == '+' || c == '!' || c == ':' || c == '|' || c == '&' || c == '-'
}

func couldBeOperator(c byte) bool {
	return couldBeOneCharOperator(c) || couldBeTwoCharOperatorBegin(c)
}

func isScoperChar(c byte) bool {
	return c == '(' || c == ')' || c == '[' || c == ']' || c == '{' || c == '}'
}

func getSpecificOperatorType(value string) TokenType {
	switch value {
	case "=":
		return TokenEqual
	case "+":
		return TokenPlus
	case "-":
		return TokenMinus
	case "+=":
		return TokenPlusEquals
	case "-=":
		return TokenMinusEquals
	case "==":
		return TokenEqualEqual
	case "!=":
		return TokenNotEqual
	case "<=":
		return TokenLessEqual
	case ">=":
		return TokenGreaterEqual
	case "<":
		return TokenLessThan
	case ">":
		return TokenGreaterThan
	case "&&":
		return TokenBooleanAnd
	case "||":
		return TokenBooleanOr
	case "!":
		return TokenBang
	case ".":
		return TokenDot
	}
	return TokenInvalid
}

func classifyToken(nextChar, followingChar byte) TokenType {
	if isASCIIDigit(nextChar) {
		return TokenInteger
	}
	if nextChar == '"' {
		return TokenString
	}

	// Note: '-' handled specially below.
	if nextChar != '-' && couldBeOperator(nextChar) {
		return TokenUnclassifiedOperator
	}

	if isIdentifierFirstChar(nextChar) {
		return TokenIdentifier
	}

	switch nextChar {
	case '[':
		return TokenLeftBracket
	case ']':
		return TokenRightBracket
	case '(':
		return TokenLeftParen
	case ')':
		return TokenRightParen
	case '{':
		return TokenLeftBrace
	case '}':
		return TokenRightBrace
	case '.':
		return TokenDot
	case ',':
		return TokenComma
	case '#':
		return TokenUnclassifiedComment
	case '-':
		// For the case of '-' differentiate between a negative number and anything
		// else.
		if followingChar == byte(0) {
			return TokenUnclassifiedOperator // Just the minus before end of file.
		}
		if isASCIIDigit(followingChar) {
			return TokenInteger
		}
		return TokenUnclassifiedOperator
	}
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

func (s *tokenizer) advanceToNextToken() {
	for !s.atEnd() && s.isCurrentWhitespace() {
		s.advance()
	}
}

func (s *tokenizer) advanceToEndOfToken(location Location, tokenType TokenType) error {
	switch tokenType {
	case TokenInteger:
		s.advance()
		for !s.atEnd() && isASCIIDigit(s.curChar()) {
			s.advance()
		}
		if !s.atEnd() {
			// Require the char after a number to be some kind of space, scope,
			// or operator.
			c := s.curChar()
			if !s.isCurrentWhitespace() && !couldBeOperator(c) && !isScoperChar(c) && c != ',' {
				return NonNumericError{
					start:   location,
					end:     s.getCurrentLocation(),
					message: "This is not a valid number.",
				}
			}
		}

	case TokenString:
		initial := s.curChar()
		s.advance() // Advance past initial "
		for {
			if s.atEnd() {
				return UnterminatedStringError{
					start:    location,
					end:      s.getCurrentLocation(),
					message:  "Unterminated string literal.",
					helpText: "Don't leave me hanging like this!",
				}
			}
			if s.isCurrentStringTerminator(initial) {
				s.advance() // Skip past last "
				break
			} else if s.isCurrentNewline() {
				return NewlineInStringConstant{
					start:   location,
					end:     s.getCurrentLocation(),
					message: "Newline in string constant.",
				}
			}
			s.advance()
		}

	case TokenUnclassifiedOperator:
		// Some operators are two characters, some are one.
		if couldBeTwoCharOperatorBegin(s.curChar()) {
			if s.canIncrement() && couldBeTwoCharOperatorEnd(s.input[s.cur+1]) {
				s.advance()
			}
		}
		s.advance()

	case TokenIdentifier:
		for !s.atEnd() && isIdentifierContinuingChar(s.curChar()) {
			s.advance()
		}

	case TokenDot,
		TokenComma,
		TokenLeftBracket, TokenRightBracket,
		TokenLeftBrace, TokenRightBrace,
		TokenLeftParen, TokenRightParen:
		s.advance() // All are one char.

	case TokenUnclassifiedComment:
		// Eat to EOL.
		for !s.atEnd() && !s.isCurrentNewline() {
			s.advance()
		}

	case TokenInvalid:
		fallthrough
	default:
		// Should not occur. Non-exhaustive switch.
		return IllegalStateError{
			start:    location,
			end:      s.getCurrentLocation(),
			message:  "Everything is all messed up",
			helpText: "Please insert system disk in drive A: and press any key.",
		}
	}
	return nil
}

func (s *tokenizer) atStartOfLine(location int) bool {
	for location > 0 {
		location--
		c := s.input[location]
		if c == '\n' {
			return true
		}
		if c != ' ' {
			return false
		}
	}
	return true
}

func (s *tokenizer) isCurrentWhitespace() bool {
	c := s.input[s.cur]
	// Note that tab (0x09), vertical tab (0x0B), and formfeed (0x0C) are illegal.
	return c == 0x0A || c == 0x0D || c == 0x20 ||
		(s.whitespaceTransform == whitespaceTransformInvalidToSpace &&
			(c == 0x09 || c == 0x0B || c == 0x0C))
}

func (s *tokenizer) isCurrentStringTerminator(quoteChar byte) bool {
	if s.curChar() != quoteChar {
		return false
	}

	// Check for escaping. \" is not a string terminator, but \\" is. Count
	// the number of preceding backslashes.
	numBackslashes := 0
	for i := s.cur - 1; i >= 0 && s.input[i] == '\\'; i-- {
		numBackslashes++
	}

	// Even backslashes mean that they were escaping each other and don't count
	// as escaping this quote.
	return (numBackslashes % 2) == 0
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
		file:         s.inputSource,
		lineNumber:   s.lineNumber,
		columnNumber: s.columnNumber,
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
	return InvalidTokenError{
		start:    location,
		message:  "Invalid token.",
		helpText: help,
	}
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
