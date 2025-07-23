// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package resolve provides an environment for executing a GN AST.
package resolve

import (
	"fmt"
	"strconv"
	"strings"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

// ExecuteNode executes a given node in the AST.
func ExecuteNode(n parse.Node, s *Scope) (Value, error) {
	switch n := n.(type) {
	case *parse.AccessorNode:
		// Accessor nodes represent either a subscript `a[b]` or member `a.b` access.
		if n.Subscript != nil {
			return executeSubscriptAccess(n, s)
		}
		if n.Member != nil {
			return executeScopeAccess(n.Base, n.Member.Value.Value(), n.Member.LocationRange(), s)
		}
		return nil, parse.MakeErrFromNode(n, syntax.ErrInvalidAST, "Invalid AST", "Found an AccessorNode without a subscript or member defined.")

	case *parse.BinaryOpNode:
		return nil, fmt.Errorf("don't know how to execute BinaryOpNode yet. got: %T(%v)", n, n)

	case *parse.BlockNode:
		// Execute in the current scope, unless the result mode is ReturnsScope.
		// Modifications will go into this also (for example, if conditions and loops).
		execScope := s
		if n.ResultMode == parse.ReturnsScope {
			// Create a nested scope to save the values for returning.
			execScope = s.NewNestedScope()
		}

		var err error
		for i := range n.Statements {
			// Check for trying to execute things with no side effects in a block.
			//
			// A BlockNode here means that somebody has a free-floating { }.
			// Technically this can have side effects since it could generated targets,
			// but we don't want to allow this since it creates ambiguity when
			// immediately following a function call that takes no block. By not
			// allowing free-floating blocks that aren't passed anywhere or assigned to
			// anything, this ambiguity is resolved.
			cur := n.Statements[i]
			switch cur.(type) {
			case *parse.ListNode, *parse.LiteralNode, *parse.UnaryOpNode, *parse.IdentifierNode, *parse.BlockNode:
				return nil, parse.MakeErrFromNode(cur,
					syntax.ErrUnknown,
					"This statement has no effect.",
					"Either delete it or do something with the result.")
			}
			_, err = ExecuteNode(cur, execScope)
			if err != nil {
				// Don't immediately return on error, if this node should return a
				// scope then the incomplete scope should be returned together.
				break
			}
		}

		if n.ResultMode == parse.ReturnsScope {
			// Clear the reference to the containing scope. This scope will be passed in
			// a value whose lifetime will not be related to the enclosing scope passed
			// to this function.
			execScope.isolate()
			return &ScopeValue{
				origin: n,
				scope:  execScope,
			}, err
		}
		return nil, err

	case *parse.FunctionCallNode:
		return nil, fmt.Errorf("don't know how to execute FunctionCallNode yet. got: %T(%v)", n, n)

	case *parse.IdentifierNode:
		return nil, fmt.Errorf("don't know how to execute IdentifierNode yet. got: %T(%v)", n, n)

	case *parse.ListNode:
		listValue := &ListValue{}
		for _, cur := range n.Contents {
			if _, ok := cur.(*parse.BlockCommentNode); ok {
				continue
			}
			value, err := ExecuteNode(cur, s)
			if err != nil {
				return nil, err
			}
			if value == nil || value.valueType() == ValueTypeNone {
				return nil, parse.MakeErrFromNode(cur, syntax.ErrTypeMismatch,
					"This does not evaluate to a value.", "I can't do something with nothing.")
			}
			listValue.list = append(listValue.list, value)
		}
		return listValue, nil

	case *parse.LiteralNode:
		switch n.Token.TokenType() {
		case syntax.TokenTrue:
			return &BooleanValue{
				origin: n,
				value:  true,
			}, nil
		case syntax.TokenFalse:
			return &BooleanValue{
				origin: n,
				value:  false,
			}, nil
		case syntax.TokenInteger:
			s := n.Token.Value()
			if (strings.HasPrefix(s, "0") && len(s) > 1) || strings.HasPrefix(s, "-0") {
				if s == "-0" {
					return nil, parse.MakeErrFromNode(n, syntax.ErrUnknown, "Negative zero doesn't make sense", "")
				}
				return nil, parse.MakeErrFromNode(n, syntax.ErrUnknown, "Leading zeros not allowed", "")
			}
			i, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, parse.MakeErrFromNode(n, syntax.ErrUnknown, "This does not look like an integer", "")
			}
			return &IntegerValue{
				origin: n,
				value:  i,
			}, nil
		case syntax.TokenString:
			// TODO: need string literal expansion.
			s := n.Token.Value()
			// Assume that the parser should have kept the quotes.
			if len(s) < 2 {
				return nil, parse.MakeErrFromNode(n, syntax.ErrUnknown, "Invalid AST", "Found a LiteralNode with an unquoted string")
			}
			s = s[1 : len(s)-1]
			return &StringValue{
				origin: n,
				value:  s,
			}, nil
		}
		return nil, parse.MakeErrFromNode(n, syntax.ErrUnknown, "Invalid AST", "Found a LiteralNode that wasn't a boolean, integer, or string")

	case *parse.BlockCommentNode:
		return nil, nil

	case *parse.ConditionNode:
		conditionResult, err := ExecuteNode(n.Condition, s)
		if err != nil {
			return nil, err
		}
		if conditionResult.valueType() != ValueTypeBoolean {
			return nil, syntax.MakeErrorAt(
				n.Condition.LocationRange().Begin(),
				[]syntax.LocationRange{n.Condition.LocationRange(), n.IfToken.Range()},
				syntax.ErrTypeMismatch,
				"Condition does not evaluate to a boolean value.",
				fmt.Sprintf("This is a value of type %q instead.", conditionResult.valueType()))
		}
		if b := conditionResult.(*BooleanValue); b.value {
			// Additional check to what C++ GN does, it always assumes the true block exists.
			if n.IfTrue == nil {
				return nil, parse.MakeErrFromNode(n, syntax.ErrUnknown, "Invalid AST", "Found a ConditionNode without true block")
			}
			// Execute the true block if the boolean evaluated to true.
			if _, err = ExecuteNode(n.IfTrue, s); err != nil {
				return nil, err
			}
		} else if n.IfFalse != nil {
			// Otherwise the else block if it exists.
			if _, err = ExecuteNode(n.IfFalse, s); err != nil {
				return nil, err
			}
		}
		// Conditionals don't return values, just cause side effects.
		return nil, nil
	}

	return nil, parse.MakeErrFromNode(n, syntax.ErrNotImplemented, fmt.Sprintf("Unimplemented node found %T(%v)", n, n), "")
}

// executeSubscriptAccess executes a subscript e.g. `a[b]` access for the parse.AccessorNode in the given scope.
// This is permitted for both lists and scopes.
func executeSubscriptAccess(n *parse.AccessorNode, scope *Scope) (Value, error) {
	baseValue := scope.Value(n.Base.Value(), false)
	if baseValue == nil {
		return nil, n.Base.MakeError(syntax.ErrUndefinedIdentifier, "Undefined identifier.")
	}
	switch baseValue.valueType() {
	case ValueTypeList:
		// Lists support zero-based subscripting to extract values.
		listValue := baseValue.(*ListValue)
		i, err := computeAndValidateListIndex(n, scope, len(listValue.list))
		if err != nil {
			return nil, err
		}
		return listValue.list[i], nil
	case ValueTypeScope:
		// Scopes support string subscripting to extract members.
		keyValue, err := ExecuteNode(n.Subscript, scope)
		if keyValue == nil {
			return nil, err
		}
		stringValue, err := AsValue[*StringValue](keyValue)
		if err != nil {
			return nil, err
		}
		return executeScopeAccess(n.Base, stringValue.value, keyValue.OriginNode().LocationRange(), scope)
	}
	return nil, n.Base.MakeError(syntax.ErrTypeMismatch,
		fmt.Sprintf("Expecting either a list or a scope for subscript, got %s.", baseValue.valueType()))
}

// executeScopeAccess executes a scope access for the base and member in the given scope.
func executeScopeAccess(baseToken syntax.Token, member string, memberRange syntax.LocationRange, scope *Scope) (Value, error) {
	if baseValue := scope.Value(baseToken.Value(), true); baseValue != nil {
		if scopeValue, ok := baseValue.(*ScopeValue); ok {
			if result := scopeValue.scope.Value(member, true); result != nil {
				return result, nil
			}
		}
	}
	// Don't return early above, instead let all cases fall back to the same error.
	// This matches C++ GN behavior.
	//
	// Given:
	//     a = { b = "c" }
	//
	// ERROR at //BUILD.gn:8:9: No value named "c" in scope "a"
	// print(a.c)
	//         ^
	// ERROR at //BUILD.gn:8:9: No value named "c" in scope "a"
	// print(a["c"])
	//         ^--
	// ERROR at //BUILD.gn:8:10: No value named "c" in scope "ab"
	// print(ab.c)
	//          ^
	//
	// Given:
	//     a = [ "b" ]
	//
	// ERROR at //BUILD.gn:10:9: No value named "b" in scope "a"
	// print(a.b)
	//         ^
	return nil, syntax.MakeErrorAt(memberRange.Begin(), []syntax.LocationRange{memberRange},
		syntax.ErrMemberNotFound,
		fmt.Sprintf("No value named %q in scope %q", member, baseToken.Value()), "")
}

func computeAndValidateListIndex(n *parse.AccessorNode, s *Scope, maxLen int) (int64, error) {
	indexValue, err := ExecuteNode(n.Subscript, s)
	if err != nil {
		return -1, err
	}
	integerValue, err := AsValue[*IntegerValue](indexValue)
	if err != nil {
		return -1, err
	}

	indexInt := integerValue.value
	if indexInt < 0 {
		return -1, parse.MakeErrFromNode(n.Subscript, syntax.ErrSubscriptOutOfRange, "Negative array subscript.",
			fmt.Sprintf("You gave me %d", indexInt))
	}
	if maxLen == 0 {
		return -1, parse.MakeErrFromNode(n.Subscript, syntax.ErrSubscriptOutOfRange, "Array subscript out of range.",
			fmt.Sprintf("You gave me %d but the array has no elements.", indexInt))
	}
	if indexInt >= int64(maxLen) {
		return -1, parse.MakeErrFromNode(n.Subscript, syntax.ErrSubscriptOutOfRange, "Array subscript out of range.",
			fmt.Sprintf("You gave me %d but I was expecting something from 0 to %d, inclusive.", indexInt, maxLen-1))
	}
	return indexInt, nil
}
