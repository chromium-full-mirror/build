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
		return nil, ASTError{
			Node:    n,
			details: "Found an AccessorNode without a subscript or member defined.",
		}

	case *parse.BinaryOpNode:
		return executeBinaryOperator(n, s)

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
				return nil, FloatingScopeError{
					Node: cur,
				}
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
		name := n.Function
		info, ok := s.Function(name.Value())
		if !ok {
			return nil, UnknownFunctionError{
				Token: name,
			}
		}
		argsValue, err := ExecuteNode(n.Args, s)
		if err != nil {
			return nil, err
		}
		args, err := AsValue[*ListValue](argsValue)
		if err != nil {
			return nil, err
		}
		switch f := info.(type) {
		case BlockFunctionInfo:
			if n.Block == nil {
				return nil, TypeError{
					Msg:              "This function call requires a block.",
					Help:             `The block's "{" must be on the same line as the function call's ")".`,
					locationOverride: n.Function.Range().Begin(),
					rangesOverride:   []syntax.LocationRange{n.Function.Range()},
				}
			}
			return f.Run(s, n, args.list, n.Block)
		case SimpleFunctionInfo:
			if n.Block != nil {
				return nil, TypeError{
					Msg: "Unexpected '{'.",
					Help: `This function call doesn't take a {} block following it, and you
can't have a {} block that's not connected to something like an if
statement or a target declaration.`,
					locationOverride: n.Block.LocationRange().Begin(),
					rangesOverride:   []syntax.LocationRange{n.Block.LocationRange()},
				}
			}
			return f.Run(s, n, args.list)
		}
		return nil, fmt.Errorf("don't know how to execute this function yet")

	case *parse.IdentifierNode:
		value := s.Value(n.Value.Value(), true)
		if value == nil {
			return nil, UndefinedIdentifierError{Token: n.Value}
		}
		// TODO: EnsureNotReadingFromSameDeclareArgs
		value.setOrigin(n)
		return value, nil

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
				return nil, TypeError{
					Value:            value,
					Msg:              "This does not evaluate to a value.",
					Help:             "I can't do something with nothing.",
					locationOverride: cur.LocationRange().Begin(),
					rangesOverride:   []syntax.LocationRange{cur.LocationRange()},
				}
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
					return nil, IntegerLiteralError{
						Node:    n,
						message: "Negative zero doesn't make sense",
					}
				}
				return nil, IntegerLiteralError{
					Node:    n,
					message: "Leading zeros not allowed",
				}
			}
			i, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, IntegerLiteralError{
					Node:    n,
					message: "This does not look like an integer",
				}
			}
			return &IntegerValue{
				origin: n,
				value:  i,
			}, nil
		case syntax.TokenString:
			str, err := expandStringLiteral(n.Token, n, s)
			if err != nil {
				return nil, err
			}
			str.setOrigin(n)
			return str, nil
		}
		return nil, ASTError{
			Node:    n,
			details: "Found a LiteralNode that wasn't a boolean, integer, or string",
		}

	case *parse.BlockCommentNode:
		return nil, nil

	case *parse.ConditionNode:
		conditionResult, err := ExecuteNode(n.Condition, s)
		if err != nil {
			return nil, err
		}
		if conditionResult.valueType() != ValueTypeBoolean {
			return nil, TypeError{
				Value:            conditionResult,
				Msg:              "Condition does not evaluate to a boolean value.",
				Help:             fmt.Sprintf("This is a value of type %q instead.", conditionResult.valueType()),
				locationOverride: n.Condition.LocationRange().Begin(),
				rangesOverride:   []syntax.LocationRange{n.Condition.LocationRange(), n.IfToken.Range()},
			}
		}
		if b := conditionResult.(*BooleanValue); b.value {
			// Additional check to what C++ GN does, it always assumes the true block exists.
			if n.IfTrue == nil {
				return nil, ASTError{
					Node:    n,
					details: "Found a ConditionNode without true block",
				}
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

	return nil, ASTError{
		Node:    n,
		details: "Unknown node type.",
	}
}

// executeSubscriptAccess executes a subscript e.g. `a[b]` access for the parse.AccessorNode in the given scope.
// This is permitted for both lists and scopes.
func executeSubscriptAccess(n *parse.AccessorNode, scope *Scope) (Value, error) {
	baseValue := scope.Value(n.Base.Value(), false)
	if baseValue == nil {
		return nil, UndefinedIdentifierError{Token: n.Base}
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
	return nil, TypeError{
		Value:            baseValue,
		Msg:              fmt.Sprintf("Expecting either a list or a scope for subscript, got %s.", baseValue.valueType()),
		locationOverride: n.Base.Range().Begin(),
		rangesOverride:   []syntax.LocationRange{n.Base.Range()},
	}
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
	return nil, KeyError{
		baseName:    baseToken.Value(),
		member:      member,
		memberRange: memberRange,
	}
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
	if maxLen == 0 || indexInt < 0 || indexInt >= int64(maxLen) {
		return -1, SubscriptError{
			Node:  n.Subscript,
			index: indexInt,
			len:   maxLen,
		}
	}
	return indexInt, nil
}
