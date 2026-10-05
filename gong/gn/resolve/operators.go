// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"fmt"
	"slices"
	"strconv"

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
	// ensureValue returns an error corresponding to this valueDestination if the
	// represented value is nil.
	//
	// Corresponds to C++ GN's ValueDestination::MakeUndefinedIdentifierForModifyError.
	ensureValue() error
	// valueForValidation returns the underlying Value if it already exists,
	// such that operations can check whether an assignment operation is legal.
	// Callers are expected not to modify the returned Value.
	// TODO: maybe this can be enforced by moving Value into a separate package?
	// alternatively handle check in assign, or new method in Value?
	//
	// Corresponds to C++ GN's ValueDestination::GetExistingValue.
	valueForValidation() Value
	// valueForMutation returns the underlying Value if it can be modified. This
	// will not search nested scopes since writes only go into the current scope.
	// Returns nil if the value does not exist, or is not in the current scope
	// (meaning assignments won't go to this value and it's not mutable). This
	// is for implementing += and -=.
	//
	// If it exists, this will mark the origin of the value to be the passed-in
	// node, and the value will be also marked unused (if possible) under the
	// assumption that it will be modified in-place.
	//
	// Corresponds to C++ GN's ValueDestination::GetExistingMutableValueIfExists.
	valueForMutation(parse.Node) Value
}

func makeIncompatibleTypeError(opNode *parse.BinaryOpNode, left, right Value) error {
	msg := fmt.Sprintf("You can't do <%s> %s <%s>.",
		left.valueType().String(), opNode.Op.Value(), right.valueType().String())
	// Extra hint for lists.
	if left.valueType() == ValueTypeList {
		msg += `

Hint: If you're attempting to add or remove a single item from a list, use "foo + [ bar ]".`
	}
	return TypeError{
		Value:            right,
		Msg:              "Incompatible types for binary operator.",
		Help:             msg,
		locationOverride: opNode.LocationRange().Begin(),
		rangesOverride:   []syntax.LocationRange{opNode.LocationRange()},
	}
}

func removeMatchesFromList(list *ListValue, toRemove Value) error {
	switch toRemove := toRemove.(type) {
	case *BooleanValue,
		*IntegerValue,
		*StringValue,
		*ScopeValue:
		beforeLen := len(list.list)
		list.list = slices.DeleteFunc(list.list, func(v Value) bool {
			return v.Equal(toRemove)
		})
		if beforeLen == len(list.list) {
			return ListRemoveNotFoundError{OriginValue: OriginValue{toRemove}}
		}
	case *ListValue:
		// Filter out each individual thing.
		// This is aggressive and will fail if any item is not found,
		// which matches C++ GN behavior.
		for _, item := range toRemove.list {
			if err := removeMatchesFromList(list, item); err != nil {
				return err
			}
		}
	}
	return nil
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
		return nil, TypeError{
			Value:            value,
			Msg:              "Operator requires a value.",
			Help:             fmt.Sprintf("This thing on the %s does not evaluate to a value.", side),
			locationOverride: opNode.LocationRange().Begin(),
			rangesOverride:   []syntax.LocationRange{opNode.LocationRange(), node.LocationRange()},
		}
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
		return nil, TypeError{
			Value:            leftValue,
			Msg:              "Left side of || operator is not a boolean.",
			Help:             fmt.Sprintf("Type is %q instead.", leftValue.valueType()),
			locationOverride: opNode.LocationRange().Begin(),
			rangesOverride:   []syntax.LocationRange{opNode.LocationRange(), leftValue.OriginNode().LocationRange()},
		}
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
		return nil, TypeError{
			Value:            rightValue,
			Msg:              "Right side of || operator is not a boolean.",
			Help:             fmt.Sprintf("Type is %q instead.", rightValue.valueType()),
			locationOverride: opNode.LocationRange().Begin(),
			rangesOverride:   []syntax.LocationRange{opNode.LocationRange(), rightValue.OriginNode().LocationRange()},
		}
	}
	return &BooleanValue{origin: opNode, value: rightBool.value}, nil
}

func executeAnd(opNode *parse.BinaryOpNode, scope *Scope) (Value, error) {
	leftValue, err := executeOpSide(opNode, sideLeft, scope)
	if err != nil {
		return nil, err
	}
	leftBool, err := AsValue[*BooleanValue](leftValue)
	if err != nil {
		return nil, TypeError{
			Value:            leftValue,
			Msg:              "Left side of && operator is not a boolean.",
			Help:             fmt.Sprintf("Type is %q instead.", leftValue.valueType()),
			locationOverride: opNode.LocationRange().Begin(),
			rangesOverride:   []syntax.LocationRange{opNode.LocationRange(), leftValue.OriginNode().LocationRange()},
		}
	}
	if !leftBool.value {
		return &BooleanValue{origin: opNode, value: false}, nil
	}

	rightValue, err := executeOpSide(opNode, sideRight, scope)
	if err != nil {
		return nil, err
	}
	rightBool, err := AsValue[*BooleanValue](rightValue)
	if err != nil {
		return nil, TypeError{
			Value:            rightValue,
			Msg:              "Right side of && operator is not a boolean.",
			Help:             fmt.Sprintf("Type is %q instead.", rightValue.valueType()),
			locationOverride: opNode.LocationRange().Begin(),
			rangesOverride:   []syntax.LocationRange{opNode.LocationRange(), rightValue.OriginNode().LocationRange()},
		}
	}
	return &BooleanValue{origin: opNode, value: rightBool.value}, nil
}

func executePlusEquals(opNode *parse.BinaryOpNode, scope *Scope) error {
	lvalue, rvalue, err := prepareAssignOp(opNode, scope)
	if err != nil {
		return err
	}

	// We need to prepare the destination for mutation.
	// First, check if the lvalue is mutable.
	dest := lvalue.valueForMutation(opNode)
	if dest == nil {
		// Looks like the lvalue isn't mutable.
		// Is it because what it points to doesn't exist at all?
		err := lvalue.ensureValue()
		if err != nil {
			return err
		}

		// No, it does exist.
		// Let's prepare the alternative destination to mutate then.
		existingValue := lvalue.valueForValidation()
		switch existingValue.valueType() {
		case ValueTypeString, ValueTypeList:
			// The lvalue is a list or string.
			// We'll create the mutable destination by copying it into the current scope.
			dest = lvalue.assign(existingValue, opNode)
		default:
			// The lvalue is something else.
			// In that case we'll always treat it as `foo = foo + bar` i.e. call executePlus,
			// then forcibly create a mutable `foo` in the current scope.
			// So use the immutable value as the "destination", and executePlus will handle the rest.
			dest = existingValue
		}
	}

	// Now that we're here, the destination to mutate has been prepared.
	if destString, ok := dest.(*StringValue); ok {
		// String + string -> string concat.
		if appendString, ok := rvalue.(*StringValue); ok {
			destString.value += appendString.value
			return nil
		}
		// String + int -> string concat.
		if appendInteger, ok := rvalue.(*IntegerValue); ok {
			destString.value += strconv.FormatInt(appendInteger.value, 10)
			return nil
		}
		return makeIncompatibleTypeError(opNode, dest, rvalue)
	} else if destList, ok := dest.(*ListValue); ok {
		// List concat. The RHS can only be a list.
		if appendList, ok := rvalue.(*ListValue); ok {
			destList.list = append(destList.list, appendList.list...)
			return nil
		}
		// This matches C++ GN's separate error message for list += invalid.
		return TypeError{
			Value:            rvalue,
			Msg:              "Incompatible types to add.",
			Help:             `To append a single item to a list do "foo += [ bar ]".`,
			locationOverride: opNode.LocationRange().Begin(),
			rangesOverride:   []syntax.LocationRange{opNode.LocationRange()},
		}
	}

	// Everything else is semantically `foo = foo + bar`.
	value, err := executePlus(opNode, dest, rvalue, false)
	if err != nil {
		return err
	}
	lvalue.assign(value, opNode)
	return nil
}

func executeMinusEquals(opNode *parse.BinaryOpNode, scope *Scope) error {
	lvalue, rvalue, err := prepareAssignOp(opNode, scope)
	if err != nil {
		return err
	}

	dest := lvalue.valueForMutation(opNode)
	if listDest, ok := dest.(*ListValue); ok {
		// The lvalue is a mutable list.
		// If the rvalue is also a list then we can optimize by in-place mutation.
		if rlist, ok := rvalue.(*ListValue); ok {
			if err := removeMatchesFromList(listDest, rlist); err != nil {
				return err
			}
			return nil
		}
	}

	// If we can't optimize above then convert `foo -= bar` to `foo = foo - bar`.
	// First make sure the lvalue exists in the first place.
	err = lvalue.ensureValue()
	if err != nil {
		return err
	}

	// It exists, so we can perform `foo = foo - bar`.
	existingValue := lvalue.valueForValidation()
	value, err := executeMinus(opNode, existingValue, rvalue)
	if err != nil {
		return err
	}
	lvalue.assign(value, opNode)
	return nil
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
			return nil, nil, UndefinedIdentifierError{Token: left.Base}
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
			return nil, nil, ASTError{
				Node:    opNode,
				details: "Got an AccessorNode without a member or subscript.",
			}
		}

	default:
		return nil, nil, ASTError{
			Node:    opNode,
			details: "Got a BinaryOpNode for assign operation where lvalue was not an ident, scope, list.",
		}
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

func executePlus(opNode *parse.BinaryOpNode, left, right Value, allowLeftTypeConversion bool) (Value, error) {
	// Left-hand-side integer.
	if lvalue, ok := left.(*IntegerValue); ok {
		if rvalue, ok := right.(*IntegerValue); ok {
			// Int + int -> addition.
			return &IntegerValue{
				origin: opNode,
				value:  lvalue.value + rvalue.value,
			}, nil
		} else if rvalue, ok := right.(*StringValue); ok && allowLeftTypeConversion {
			// Int + string -> string concat.
			return &StringValue{
				origin: opNode,
				value:  fmt.Sprintf("%d%s", lvalue.value, rvalue.value),
			}, nil
		}
		return nil, makeIncompatibleTypeError(opNode, left, right)
	}

	// Left-hand-side string.
	if lvalue, ok := left.(*StringValue); ok {
		if rvalue, ok := right.(*IntegerValue); ok {
			// String + int -> string concat.
			return &StringValue{
				origin: opNode,
				value:  lvalue.value + fmt.Sprintf("%d", rvalue.value),
			}, nil
		} else if rvalue, ok := right.(*StringValue); ok {
			// String + string -> string concat. Since the left is passed by copy
			// we can avoid realloc if there is enough buffer by appending to left
			// and assigning.
			lvalue.value += rvalue.value
			return lvalue, nil
		}
		return nil, makeIncompatibleTypeError(opNode, left, right)
	}

	// Left-hand-side list. The only valid thing is to add another list.
	if lvalue, ok := left.(*ListValue); ok {
		if rvalue, ok := right.(*ListValue); ok {
			lvalue.list = append(lvalue.list, rvalue.list...)
			return lvalue, nil
		}
	}

	return nil, makeIncompatibleTypeError(opNode, left, right)
}

func executeMinus(opNode *parse.BinaryOpNode, left, right Value) (Value, error) {
	// Left-hand-side int. The only thing to do is subtract another int.
	if lvalue, ok := left.(*IntegerValue); ok {
		if rvalue, ok := right.(*IntegerValue); ok {
			// Int - int -> subtraction.
			return &IntegerValue{
				origin: opNode,
				value:  lvalue.value - rvalue.value,
			}, nil
		}
	}

	// Left-hand-side list. The only thing to do is subtract another list.
	if lvalue, ok := left.(*ListValue); ok {
		if rvalue, ok := right.(*ListValue); ok {
			// In-place modify left and return it.
			if err := removeMatchesFromList(lvalue, rvalue); err != nil {
				return nil, err
			}
			return lvalue, nil
		}
	}

	return nil, makeIncompatibleTypeError(opNode, left, right)
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
			return nil, TypeError{
				Value:            rvalue,
				Msg:              fmt.Sprintf("Replacing nonempty %s.", oldValue.valueType().String()),
				Help:             fmt.Sprintf("This overwrites a previously-defined nonempty %s.", oldValue.valueType().String()),
				locationOverride: opNode.LocationRange().Begin(),
				rangesOverride:   []syntax.LocationRange{opNode.LocationRange()},
			}
		}
		// Validation passed, perform the assignment.
		return lvalue.assign(rvalue, opNode.Right), nil

	case syntax.TokenPlusEquals:
		return nil, executePlusEquals(opNode, scope)

	case syntax.TokenMinusEquals:
		return nil, executeMinusEquals(opNode, scope)

	// ||, &&.
	case syntax.TokenBooleanOr:
		return executeOr(opNode, scope)
	case syntax.TokenBooleanAnd:
		return executeAnd(opNode, scope)
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
	case syntax.TokenPlus:
		return executePlus(opNode, leftValue, rightValue, true)
	case syntax.TokenMinus:
		return executeMinus(opNode, leftValue, rightValue)

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
		lv, okL := leftValue.(*IntegerValue)
		rv, okR := rightValue.(*IntegerValue)
		if !okL || !okR {
			te := TypeError{
				locationOverride: opNode.LocationRange().Begin(),
				rangesOverride: []syntax.LocationRange{
					opNode.LocationRange(),
					leftValue.OriginNode().LocationRange(),
					rightValue.OriginNode().LocationRange(),
				},
				Msg:  "Comparison requires two integers.",
				Help: "This operator can only compare two integers.",
			}
			if !okL {
				te.Value = leftValue
			} else if !okR {
				te.Value = rightValue
			}
			return nil, te
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

	return nil, ASTError{
		Node:    opNode,
		details: fmt.Sprintf("Unrecognized binary operation %q", opNode.Op.Value()),
	}
}
