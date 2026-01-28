// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package parse converts GN syntax tokens into an AST.
package parse

import (
	"errors"
	"fmt"

	"go.chromium.org/build/gong/gn/syntax"
)

type parser struct {
	tokens              []syntax.Token
	lineCommentTokens   []syntax.Token
	suffixCommentTokens []syntax.Token
	cur                 int
}

// Precedence constants.
//
// Currently all operators are left-associative so this list is sequential. To
// implement a right-associative operators in a Pratt parser we would leave gaps
// in between the constants, and right-associative operators get a precedence
// of "<left-associated-precedence> - 1".
type precedence int

const precedenceInvalid = -1
const (
	precedenceNone precedence = iota
	precedenceAssignment
	precedenceOr
	precedenceAnd
	precedenceEquality
	precedenceRelation
	precedenceSum
	precedencePrefix
	precedenceCall
	precedenceDot
)

// Parse converts a series of tokens into an AST.
func Parse(tokens []syntax.Token) (Node, error) {
	p := parser{}
	// Collect line and suffix comments now so that they can be attached to the
	// nearest appropriate node after building the AST.
	// We don't want to concern ourselves with these calculations whilst
	// building the AST.
	for _, token := range tokens {
		switch token.TokenType() {
		case syntax.TokenLineComment:
			p.lineCommentTokens = append(p.lineCommentTokens, token)
		case syntax.TokenSuffixComment:
			p.suffixCommentTokens = append(p.suffixCommentTokens, token)
		default:
			p.tokens = append(p.tokens, token)
		}
	}
	return p.parseFile()
}

func (p *parser) curToken() syntax.Token {
	return p.tokens[p.cur]
}

func (p *parser) curOrLastToken() syntax.Token {
	if p.atEnd() {
		return p.tokens[len(p.tokens)-1]
	}
	return p.curToken()
}

func (p *parser) atEnd() bool {
	return p.cur >= len(p.tokens)
}

func (p *parser) isAssignment(node Node) bool {
	if binaryOp, ok := node.(*BinaryOpNode); ok {
		return binaryOp.Op.TokenType() == syntax.TokenEqual ||
			binaryOp.Op.TokenType() == syntax.TokenPlusEquals ||
			binaryOp.Op.TokenType() == syntax.TokenMinusEquals
	}
	return false
}

func (p *parser) isStatementBreak(tokenType syntax.TokenType) bool {
	switch tokenType {
	case syntax.TokenIdentifier,
		syntax.TokenLeftBrace,
		syntax.TokenRightBrace,
		syntax.TokenIf,
		syntax.TokenElse:
		return true
	}
	return false
}

func (p *parser) lookAhead(tokenType syntax.TokenType) bool {
	if p.atEnd() {
		return false
	}
	return p.curToken().TokenType() == tokenType
}

func (p *parser) consumeOnly(tokenType syntax.TokenType) (syntax.Token, bool) {
	if !p.lookAhead(tokenType) {
		return syntax.Token{}, false
	}
	return p.consume()
}

func (p *parser) consume() (syntax.Token, bool) {
	if p.atEnd() {
		return syntax.Token{}, false
	}
	token := p.tokens[p.cur]
	p.cur++
	return token, true
}

func (p *parser) parseFile() (Node, error) {
	file := BlockNode{
		ResultMode: DiscardsResult,
	}
	for !p.atEnd() {
		statement, err := p.parseStatement()
		// TODO: use go1.26 errors.AsType once available
		// https://go.dev/doc/go1.26#errorspkgerrors
		var e *EOF
		if errors.As(err, &e) {
			break
		}
		if err != nil {
			return nil, err
		}
		file.appendStatement(statement)
	}
	// Newline expected to determine the end of expressions inside blocks, so
	// if nil statement found but not at end of file, then \n is missing.
	// https://gn.googlesource.com/gn/+/26baf4ba17029ceabd1b4aef1ab8690e13e58a63
	if !p.atEnd() {
		return nil, TokenError{
			token:   p.curOrLastToken(),
			message: "Unexpected here, should be newline.",
		}
	}
	return &file, nil
}

func (p *parser) parseStatement() (Node, error) {
	// GN handles ifs with recursive descent, block comments directly.
	// https://source.chromium.org/gn/gn/+/main:src/gn/parser.cc;l=711-714;drc=4a64809c3631bd4365738ff8764cf357d9e80dbf
	if p.lookAhead(syntax.TokenIf) {
		return p.parseCondition()
	} else if token, ok := p.consumeOnly(syntax.TokenBlockComment); ok {
		return &BlockCommentNode{token}, nil
	}
	// GN handles everything else as a Pratt parser.
	return p.parseExpression(precedenceNone)
}

func (p *parser) parseCondition() (Node, error) {
	conditionNode := &ConditionNode{}

	// Consume "if ("
	if ifToken, ok := p.consumeOnly(syntax.TokenIf); !ok {
		return nil, TokenError{
			token:   p.curOrLastToken(),
			message: "Expected 'if'",
		}
	} else {
		conditionNode.IfToken = ifToken
	}
	if _, ok := p.consumeOnly(syntax.TokenLeftParen); !ok {
		return nil, TokenError{
			token:   p.curOrLastToken(),
			message: "Expected '(' after 'if'",
		}
	}

	// Consume conditional.
	if condition, err := p.parseExpression(precedenceNone); err != nil {
		return nil, err
	} else {
		conditionNode.Condition = condition
	}
	// TODO: Don't allow assignments in parseExpression instead?
	// https://gn.googlesource.com/gn/+/main/docs/reference.md#Grammar
	if p.isAssignment(conditionNode.Condition) {
		return nil, NodeError{
			OriginNode: OriginNode{conditionNode.Condition},
			message:    "Assignment not allowed in 'if'",
		}
	}

	// Consume ")".
	if _, ok := p.consumeOnly(syntax.TokenRightParen); !ok {
		return nil, TokenError{
			token:   p.curOrLastToken(),
			message: "Expected ')' after condition of 'if'",
		}
	}

	// Consume the true block.
	if leftBrace, ok := p.consumeOnly(syntax.TokenLeftBrace); !ok {
		return nil, TokenError{
			token:   p.curOrLastToken(),
			message: "Expected '{' to start 'if' block",
		}
	} else {
		if block, err := p.parseBlock(leftBrace, DiscardsResult); err == nil {
			conditionNode.IfTrue = block
		} else {
			return nil, err
		}
	}

	// Consume "else" if it exists.
	if _, ok := p.consumeOnly(syntax.TokenElse); ok {
		if leftBrace, ok := p.consumeOnly(syntax.TokenLeftBrace); ok {
			// There was "{", this is just an else block.
			if block, err := p.parseBlock(leftBrace, DiscardsResult); err == nil {
				conditionNode.IfFalse = block
			}
		} else if p.lookAhead(syntax.TokenIf) {
			// There was "if", it's an else if. Parse that condition.
			if subCondition, err := p.parseCondition(); err != nil {
				return nil, err
			} else {
				conditionNode.IfFalse = subCondition
			}
		} else {
			return nil, TokenError{
				token:   p.curOrLastToken(),
				message: "Expected '{' or 'if' after 'else'",
			}
		}
	}

	return conditionNode, nil
}

func (p *parser) parseExpression(precedence precedence) (Node, error) {
	token, ok := p.consume()
	if !ok {
		return nil, EOF{token: p.curOrLastToken()}
	}

	left, err := p.parsePrefix(token)
	if err != nil {
		return nil, err
	}

	for !p.atEnd() && !p.isStatementBreak(p.curToken().TokenType()) && precedence <= p.infixPrecedence(p.curToken()) {
		next, _ := p.consume()
		left, err = p.parseInfix(left, next)
		if err != nil {
			return nil, err
		}
	}

	return left, nil
}

func (p *parser) parsePrefix(token syntax.Token) (Node, error) {
	switch token.TokenType() {
	case syntax.TokenInteger,
		syntax.TokenString,
		syntax.TokenTrue,
		syntax.TokenFalse:
		return &LiteralNode{token}, nil
	case syntax.TokenBang:
		// !foo
		expr, err := p.parseExpression(precedenceCall)
		if err != nil {
			return nil, err
		}
		return &UnaryOpNode{
			Op:      token,
			Operand: expr,
		}, nil
	case syntax.TokenLeftParen:
		// (foo)
		expr, err := p.parseExpression(precedenceNone)
		if err != nil {
			return nil, err
		}
		if _, ok := p.consumeOnly(syntax.TokenRightParen); !ok {
			return nil, TokenError{
				token:   p.curToken(),
				message: "Expected ')'",
			}
		}
		return expr, nil
	case syntax.TokenLeftBracket:
		// [foo]
		list, err := p.parseList(token, syntax.TokenRightBracket, true)
		if err != nil {
			return nil, err
		}
		if _, ok := p.consumeOnly(syntax.TokenRightBracket); !ok {
			return nil, TokenError{
				token:   p.curToken(),
				message: "Expected ']'",
			}
		}
		return &list, nil
	case syntax.TokenLeftBrace:
		// {foo}
		return p.parseBlock(token, ReturnsScope)
	case syntax.TokenIdentifier:
		return p.parseIdentifierOrCall(nil, token)
	case syntax.TokenBlockComment:
		return &BlockCommentNode{token}, nil
	}
	return nil, TokenError{
		token: token,
		// Error with single quotes to match GN, rather than using %q.
		message: fmt.Sprintf("Unexpected token '%s'", token.Value()),
	}
}

func (p *parser) parseInfix(left Node, token syntax.Token) (Node, error) {
	switch token.TokenType() {
	case syntax.TokenEqual,
		syntax.TokenPlusEquals,
		syntax.TokenMinusEquals:
		_, isIdentifier := left.(*IdentifierNode)
		_, isAccessor := left.(*AccessorNode)
		if !isIdentifier && !isAccessor {
			return nil, NodeError{
				OriginNode: OriginNode{left},
				message:    "The left-hand side of an assignment must be an identifier, scope access, or array access.",
			}
		}
		value, err := p.parseExpression(precedenceAssignment)
		if err != nil {
			return nil, err
		}
		if value == nil {
			return nil, TokenError{
				token:   token,
				message: "Expected right-hand side of assignment.",
			}
		}
		return &BinaryOpNode{
			Op:    token,
			Left:  left,
			Right: value,
		}, nil
	case syntax.TokenDot:
		leftIdentifier, isIdentifier := left.(*IdentifierNode)
		if !isIdentifier {
			return nil, NodeError{
				OriginNode: OriginNode{left},
				message:    `May only use "." for identifiers.`,
				helpText:   "The thing on the left hand side of the dot must be an identifier\nand not an expression. If you need this, you'll have to assign the\nvalue to a temporary first. Sorry.",
			}
		}
		right, err := p.parseExpression(precedenceDot)
		if err != nil {
			return nil, err
		}
		rightIdentifier, isIdentifier := right.(*IdentifierNode)
		if !isIdentifier {
			return nil, TokenError{
				token:    token,
				message:  `Expected identifier for right-hand-side of "."`,
				helpText: "Good: a.cookies\nBad: a.42\nLooks good but still bad: a.cookies()",
			}
		}
		return &AccessorNode{
			Base:   leftIdentifier.Value,
			Member: rightIdentifier,
		}, nil
	case syntax.TokenLeftBracket:
		leftIdentifier, isIdentifier := left.(*IdentifierNode)
		if !isIdentifier {
			return nil, NodeError{
				OriginNode: OriginNode{left},
				message:    "May only subscript identifiers.",
				helpText:   "The thing on the left hand side of the [] must be an identifier\nand not an expression. If you need this, you'll have to assign the\nvalue to a temporary before subscripting. Sorry.",
			}
		}
		value, err := p.parseExpression(precedenceNone)
		if err != nil {
			return nil, err
		}
		if _, ok := p.consumeOnly(syntax.TokenRightBracket); !ok {
			return nil, TokenError{
				token:   p.curToken(),
				message: "Expecting ']' after subscript.",
			}
		}
		return &AccessorNode{
			Base:      leftIdentifier.Value,
			Subscript: value,
		}, nil
	case syntax.TokenPlus,
		syntax.TokenMinus,
		syntax.TokenEqualEqual,
		syntax.TokenNotEqual,
		syntax.TokenLessEqual,
		syntax.TokenGreaterEqual,
		syntax.TokenLessThan,
		syntax.TokenGreaterThan,
		syntax.TokenBooleanAnd,
		syntax.TokenBooleanOr:
		right, err := p.parseExpression(p.infixPrecedence(token) + 1)
		if err != nil {
			// TODO: use go1.26 errors.AsType once available
			// https://go.dev/doc/go1.26#errorspkgerrors
			var e *EOF
			if errors.As(err, &e) {
				return nil, TokenError{
					token: token,
					// Error with single quotes to match GN, rather than using %q.
					message: fmt.Sprintf("Expected right-hand side for '%s'.", token.Value()),
				}
			}
			return nil, err
		}
		return &BinaryOpNode{
			Op:    token,
			Left:  left,
			Right: right,
		}, nil
	case syntax.TokenIdentifier:
		return p.parseIdentifierOrCall(left, token)
	}
	return nil, TokenError{
		token:   token,
		message: fmt.Sprintf("don't know how to parse %q yet", token.Value()),
	}
}

func (p *parser) infixPrecedence(token syntax.Token) precedence {
	switch token.TokenType() {
	case syntax.TokenEqual,
		syntax.TokenPlusEquals, syntax.TokenMinusEquals:
		return precedenceAssignment
	case syntax.TokenPlus, syntax.TokenMinus:
		return precedenceSum
	case syntax.TokenEqualEqual, syntax.TokenNotEqual:
		return precedenceEquality
	case syntax.TokenLessEqual, syntax.TokenGreaterEqual,
		syntax.TokenLessThan, syntax.TokenGreaterThan:
		return precedenceRelation
	case syntax.TokenBooleanAnd:
		return precedenceAnd
	case syntax.TokenBooleanOr:
		return precedenceOr
	case syntax.TokenDot:
		return precedenceDot
	case syntax.TokenLeftBracket, syntax.TokenIdentifier:
		return precedenceCall
	}
	return precedenceInvalid
}

func (p *parser) parseIdentifierOrCall(left Node, token syntax.Token) (Node, error) {
	var err error
	var args *ListNode
	var block *BlockNode
	hasArg := false
	if leftParen, ok := p.consumeOnly(syntax.TokenLeftParen); ok {
		// Parsing a function call.
		hasArg = true
		if _, ok = p.consumeOnly(syntax.TokenRightParen); !ok {
			// Didn't see ) straight away.
			// This means function call with arguments.
			// Parse the argument list in order to create the AST node.
			parsedList, err := p.parseList(leftParen, syntax.TokenRightParen, false)
			if err != nil {
				return nil, err
			}
			if _, ok := p.consumeOnly(syntax.TokenRightParen); !ok {
				return nil, TokenError{
					token:   token,
					message: "Expected ')' after call",
				}
			}
			args = &parsedList
		} else {
			// Got a ) straight away.
			// This means an empty function call.
			// GN still creates a list in the AST, but unintuitively with the identifier as the
			// start and end of the node in this case. Here, this behavior is replicated.
			// (This would mean the EndNode which the comment processing phase attaches comments
			// to would be the identifier itself, rather than the blank "()".
			// Note this also means the same identifier is referenced three times in total:
			// - FunctionCallNode's function refers to the identifier
			// - FunctionCallNode's args i.e. this ListNode refers to the identifier for begin
			// - FunctionCallNode's args i.e. this ListNode refers to the identifier for end
			// TODO: Do we really need to replicate this behavior?)
			args = &ListNode{
				BeginToken: token,
				End:        EndNode{token},
			}
		}
		// Optionally with a scope.
		if token, ok := p.consumeOnly(syntax.TokenLeftBrace); ok {
			block, err = p.parseBlock(token, DiscardsResult)
			if err != nil {
				return nil, err
			}
		}
	}

	if left == nil && !hasArg {
		// Not a function call, just a standalone identifier
		return &IdentifierNode{token}, nil
	}
	funcCall := &FunctionCallNode{}
	funcCall.Function = token
	funcCall.Args = args
	if block != nil {
		funcCall.Block = block
	}
	return funcCall, nil
}

func (p *parser) parseList(startToken syntax.Token, stopBefore syntax.TokenType, allowTrailingComma bool) (ListNode, error) {
	list := ListNode{}
	list.BeginToken = startToken
	lastWasComma := false
	firstTime := true
	for !p.lookAhead(stopBefore) {
		if !firstTime && !lastWasComma {
			// Require commas separate things in lists.
			return ListNode{}, TokenError{
				token:   startToken,
				message: "Expected comma between items.",
			}
		}
		firstTime = false

		// Why OR? We're parsing things that are higher precedence than the ,
		// that separates the items of the list. , should appear lower than
		// boolean expressions (the lowest of which is OR), but above assignments.
		expr, err := p.parseExpression(precedenceOr)
		if err != nil {
			return ListNode{}, err
		}
		list.appendItem(expr)
		if p.atEnd() {
			return ListNode{}, TokenError{
				token:   startToken,
				message: "Unexpected end of file in list.",
			}
		}
		// TODO: If GN sees BlockCommentNode as last node it will pretend a
		// comma was received, do we need to replicate this behavior?
		_, lastWasComma = p.consumeOnly(syntax.TokenComma)
	}
	if lastWasComma && !allowTrailingComma {
		return ListNode{}, TokenError{
			token:   startToken,
			message: "Trailing comma",
		}
	}
	// Do not consume end node, this should be responsibility of the caller.
	list.End = EndNode{p.curToken()}
	return list, nil
}

func (p *parser) parseBlock(beginBrace syntax.Token, resultMode BlockNodeResultMode) (*BlockNode, error) {
	block := &BlockNode{
		BeginToken: beginBrace,
		ResultMode: resultMode,
	}
	for {
		if token, ok := p.consumeOnly(syntax.TokenRightBrace); ok {
			block.End = EndNode{token}
			break
		}

		statement, err := p.parseStatement()
		if err != nil {
			return nil, err
		}
		block.appendStatement(statement)
	}
	return block, nil
}
