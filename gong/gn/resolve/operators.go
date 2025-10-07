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
	case syntax.TokenEqual:
		switch left := opNode.Left.(type) {
		case *parse.IdentifierNode:
			ident := left.Value.Value()
			rvalue, err := ExecuteNode(opNode.Right, scope)
			if err != nil {
				return nil, err
			}
			if rvalue.valueType() == ValueTypeNone {
				return nil, fmt.Errorf("operator requires a rvalue")
			}

			// GN does not allow clobbering non-empty lists/scopes with another non-empty list/scope.
			// The expectation is that users generally want to append to lists and merge scopes, not
			// replace them. If the user really wants to replace, they're asked to first overwrite the
			// value with an empty list/scope.
			oldValue := scope.Value(ident, true)
			isClobber := false
			switch lv := oldValue.(type) {
			case *ListValue:
				if rv, ok := rvalue.(*ListValue); ok && len(lv.list) > 0 && len(rv.list) > 0 {
					isClobber = true
				}
			case *ScopeValue:
				if rv, ok := rvalue.(*ScopeValue); ok && lv.scope.HasValues() && rv.scope.HasValues() {
					isClobber = true
				}
			}
			if isClobber {
				// TODO: to match GN precisely, need to also add a sub-error with hint about how to fix
				// e.g. if you really wanted to do this then you must run `foo = []` first.
				return nil, syntax.MakeErrorAt(opNode.LocationRange().Begin(), nil,
					syntax.ErrInvalidOperation,
					fmt.Sprintf("Replacing nonempty %s.", oldValue.valueType().String()),
					fmt.Sprintf("This overwrites a previously-defined nonempty %s.", oldValue.valueType().String()))
			}

			// Validation passed, perform the assignment.
			scope.values[ident] = record{
				used:  false,
				value: rvalue.CopyWithOrigin(opNode.Right),
			}
			return scope.values[ident].value, nil
		case *parse.AccessorNode:
			return nil, parse.MakeErrFromNode(opNode, syntax.ErrNotImplemented,
				"Not implemented", "= with a.b or a[b] on LHS isn't implemented yet.")
		}

	case syntax.TokenPlusEquals,
		syntax.TokenMinusEquals:
		return nil, parse.MakeErrFromNode(opNode, syntax.ErrNotImplemented,
			"Not implemented", "+= and -= aren't implemented yet.")

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
