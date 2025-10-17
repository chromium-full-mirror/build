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

// valueDestination represents the lvalue in =, +=, and -= operations, where the lvalue can be an identifier
// e.g. `a = 42`, or the results of evaluating an AccessorNode e.g. `a.b = 42` or `a[b] = 42`.
type valueDestination interface {
	assign(newValue Value, origin parse.Node) Value
	// valueForValidation returns the underlying Value if it already exists,
	// such that operations can check whether an assignment operation is legal.
	// Callers are expected not to modify the returned Value.
	// TODO: maybe this can be enforced by moving Value into a separate package?
	// alternatively handle check in assign, or new method in Value?
	valueForValidation() Value
}

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

// prepareAssignOp prepares lvalue and rvalue for =, +=, -= operations.
func prepareAssignOp(opNode *parse.BinaryOpNode, scope *Scope) (lvalue valueDestination, rvalue Value, err error) {
	// First prepare lvalue.
	switch left := opNode.Left.(type) {
	case *parse.IdentifierNode:
		lvalue = scope.access(left.Value)

	case *parse.AccessorNode:
		baseStr := left.Base.Value()
		// Only allow mutations `a.b = c` or `a[b] = c` where `a` is in this scope.
		base := scope.valueInCurrentScope(baseStr, false)
		if base == nil {
			// TODO(b/388723392): GN makes an error "Suspicious in-place modification" with a detailed
			// help message if the value is found in a parent scope.
			// But we don't support import() yet, so there's no point in doing this currently.
			return nil, nil, left.Base.MakeError(syntax.ErrUndefinedIdentifier, "Undefined identifier.")
		}
		if left.Subscript != nil {
			// List access `a[b] = c`, where base = `a`.
			listValue, err := AsValue[*ListValue](base)
			if err != nil {
				// TODO(b/388723392): C++ GN has to rewrite the error location here because it will
				// end up pointing at the original variable declaration instead of usage.
				// Do we also need to perform a similar check?
				return nil, nil, err
			}
			// Need to evaluate the subscript (`b` in `a[b] = c`).
			indexValue, err := ExecuteNode(left.Subscript, scope)
			if err != nil {
				return nil, nil, err
			}
			index, err := AsValue[*IntegerValue](indexValue)
			if err != nil {
				return nil, nil, err
			}
			// Finally we have concrete list `a` and integer `b`.
			lvalue, err = listValue.access(index.value, left.Subscript)
			if err != nil {
				return nil, nil, err
			}
		} else if left.Member != nil {
			// Scope access `a.b = c`, where base = `a`.
			scopeValue, err := AsValue[*ScopeValue](base)
			if err != nil {
				// TODO(b/388723392): C++ GN has to rewrite the error location here because it will
				// end up pointing at the original variable declaration instead of usage.
				// Do we also need to perform a similar check?
				return nil, nil, err
			}
			// Finally we have concrete scope `a` and identifier `b`.
			lvalue = scopeValue.scope.access(left.Member.Value)
		} else {
			return nil, nil, parse.MakeErrFromNode(opNode, syntax.ErrInvalidAST,
				"Invalid AST", "Got an AccessorNode without a member or subscript.")
		}

	default:
		return nil, nil, parse.MakeErrFromNode(opNode, syntax.ErrTypeMismatch,
			"Invalid AST", "Got a BinaryOpNode for assign operation where lvalue was not an ident, scope, list.")
	}
	// Then prepare rvalue.
	rvalue, err = ExecuteNode(opNode.Right, scope)
	if err != nil {
		return nil, nil, err
	}
	if rvalue.valueType() == ValueTypeNone {
		return nil, nil, fmt.Errorf("operator requires a rvalue")
	}
	return lvalue, rvalue, nil
}

func executeBinaryOperator(opNode *parse.BinaryOpNode, scope *Scope) (Value, error) {
	// Operators that do not require pre-evaluation of both LHS/RHS.
	switch opNode.Op.TokenType() {
	case syntax.TokenEqual:
		lvalue, rvalue, err := prepareAssignOp(opNode, scope)
		if err != nil {
			return nil, err
		}
		// GN does not allow clobbering non-empty lists/scopes with another non-empty list/scope.
		// The expectation is that users generally want to append to lists and merge scopes, not
		// replace them. If the user really wants to replace, they're asked to first overwrite the
		// value with an empty list/scope.
		oldValue := lvalue.valueForValidation()
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
		return lvalue.assign(rvalue, opNode.Right), nil

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
