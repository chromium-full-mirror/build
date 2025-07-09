// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package resolve provides an environment for executing a GN AST.
package resolve

import (
	"fmt"

	"go.chromium.org/build/gong/gn/parse"
)

// ExecuteNode executes a given node in the AST.
func ExecuteNode(n parse.ParseNode, s *Scope) (Value, error) {
	switch n := n.(type) {
	case *parse.AccessorNode:
		return nil, fmt.Errorf("don't know how to execute AccessorNode yet. got: %T(%v)", n, n)

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
				return nil, parse.MakeErrFromParseNode(cur,
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
		return nil, fmt.Errorf("don't know how to execute ListNode yet. got: %T(%v)", n, n)

	case *parse.LiteralNode:
		return nil, fmt.Errorf("don't know how to execute LiteralNode yet. got: %T(%v)", n, n)

	case *parse.BlockCommentNode:
		return nil, fmt.Errorf("don't know how to execute BlockCommentNode yet. got: %T(%v)", n, n)

	case *parse.ConditionNode:
		return nil, fmt.Errorf("don't know how to execute ConditionNode yet. got: %T(%v)", n, n)
	}

	return nil, parse.MakeErrFromParseNode(n, fmt.Sprintf("Unimplemented node found %T(%v)", n, n), "")
}
