// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"fmt"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

type side string

const (
	sideLeft  side = "left"
	sideRight side = "right"
)

func executeOpSide(opNode *parse.BinaryOpNode, side side, scope *Scope) (Value, error) {
	var node parse.Node
	switch side {
	case sideLeft:
		node = opNode.Left
	case sideRight:
		node = opNode.Right
	default:
		return nil, fmt.Errorf("%q is not a valid side", side)
	}
	value, err := ExecuteNode(node, scope)
	if err != nil {
		return nil, err
	}
	if value == nil || value.valueType() == ValueTypeNone {
		return nil, syntax.MakeErrorAt(
			opNode.LocationRange().Begin(),
			[]syntax.LocationRange{opNode.LocationRange(), node.LocationRange()},
			syntax.ErrTypeMismatch,
			"Operator requires a value.",
			fmt.Sprintf("This thing on the %s does not evaluate to a value.", side))
	}
	return value, nil
}

func executeOr(opNode *parse.BinaryOpNode, scope *Scope) (Value, error) {
	leftValue, err := executeOpSide(opNode, sideLeft, scope)
	if err != nil {
		return nil, err
	}
	leftBool, err := AsValue[*BooleanValue](leftValue)
	if err != nil {
		return nil, parse.MakeErrFromNode(opNode, syntax.ErrTypeMismatch,
			"Left side of || operator is not a boolean.",
			fmt.Sprintf("Type is %q instead.", leftValue.valueType()))
	}
	if leftBool.value {
		return &BooleanValue{origin: opNode, value: true}, nil
	}

	rightValue, err := executeOpSide(opNode, sideRight, scope)
	if err != nil {
		return nil, err
	}
	rightBool, err := AsValue[*BooleanValue](rightValue)
	if err != nil {
		return nil, parse.MakeErrFromNode(opNode, syntax.ErrTypeMismatch,
			"Right side of || operator is not a boolean.",
			fmt.Sprintf("Type is %q instead.", rightValue.valueType()))
	}
	return &BooleanValue{origin: opNode, value: rightBool.value}, nil
}

func executeBinaryOperator(opNode *parse.BinaryOpNode, scope *Scope) (Value, error) {
	// Operators that do not require pre-evaluation of both LHS/RHS.
	switch opNode.Op.TokenType() {
	case syntax.TokenEqual,
		syntax.TokenPlusEquals,
		syntax.TokenMinusEquals:
		return nil, parse.MakeErrFromNode(opNode, syntax.ErrNotImplemented,
			"Not implemented", "Operators mutating an lvalue aren't implemented yet.")

	// ||, &&.
	case syntax.TokenBooleanOr:
		return executeOr(opNode, scope)
	case syntax.TokenBooleanAnd:
		return nil, parse.MakeErrFromNode(opNode, syntax.ErrNotImplemented,
			"Not implemented", "&& isn't implemented yet.")
	}

	// All other operators require pre-evaluation of both LHS/RHS.
	leftValue, err := executeOpSide(opNode, sideLeft, scope)
	if err != nil {
		return nil, err
	}
	rightValue, err := executeOpSide(opNode, sideRight, scope)
	if err != nil {
		return nil, err
	}

	switch opNode.Op.TokenType() {
	// +, -.
	case syntax.TokenMinus:
		return nil, parse.MakeErrFromNode(opNode, syntax.ErrNotImplemented,
			"Not implemented", "- isn't implemented yet.")
	case syntax.TokenPlus:
		return nil, parse.MakeErrFromNode(opNode, syntax.ErrNotImplemented,
			"Not implemented", "+ isn't implemented yet.")

	// ==, !=.
	case syntax.TokenEqualEqual:
		return &BooleanValue{origin: opNode, value: leftValue.Equal(rightValue)}, nil
	case syntax.TokenNotEqual:
		return &BooleanValue{origin: opNode, value: !leftValue.Equal(rightValue)}, nil

	// >=, <=, >, <.
	case syntax.TokenGreaterEqual,
		syntax.TokenLessEqual,
		syntax.TokenGreaterThan,
		syntax.TokenLessThan:
		lv, ok1 := leftValue.(*IntegerValue)
		rv, ok2 := rightValue.(*IntegerValue)
		if !ok1 || !ok2 {
			return nil, syntax.MakeErrorAt(
				opNode.LocationRange().Begin(),
				[]syntax.LocationRange{
					opNode.LocationRange(),
					leftValue.OriginNode().LocationRange(),
					rightValue.OriginNode().LocationRange(),
				},
				syntax.ErrTypeMismatch,
				"Comparison requires two integers.",
				"This operator can only compare two integers.")
		}
		switch opNode.Op.TokenType() {
		case syntax.TokenGreaterEqual:
			return &BooleanValue{origin: opNode, value: lv.value >= rv.value}, nil
		case syntax.TokenLessEqual:
			return &BooleanValue{origin: opNode, value: lv.value <= rv.value}, nil
		case syntax.TokenGreaterThan:
			return &BooleanValue{origin: opNode, value: lv.value > rv.value}, nil
		case syntax.TokenLessThan:
			return &BooleanValue{origin: opNode, value: lv.value < rv.value}, nil
		default:
			return nil, fmt.Errorf("non-exhaustive switch")
		}
	}

	return nil, parse.MakeErrFromNode(opNode, syntax.ErrInvalidAST,
		"Invalid AST", fmt.Sprintf("Unrecognized binary operation %q", opNode.Op.Value()))
}
