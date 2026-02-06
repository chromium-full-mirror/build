// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package parse

import "go.chromium.org/build/gong/gn/syntax"

// Node is a node in the AST.
type Node interface {
	// LocationRange is the file range this node represents.
	LocationRange() syntax.LocationRange
	// Dump returns a JSON-serializable representation of this node.
	Dump() NodeDump
}

// AccessorNode represents accessing an array or scope element.
type AccessorNode struct {
	Base      syntax.Token
	Subscript Node
	Member    *IdentifierNode
}

// LocationRange returns the location range for this node.
func (n *AccessorNode) LocationRange() syntax.LocationRange {
	if n.Subscript != nil {
		return n.Base.Range().Union(n.Subscript.LocationRange())
	} else if n.Member != nil {
		return n.Base.Range().Union(n.Member.LocationRange())
	}
	return n.Base.Range()
}

// BlockNodeResultMode sets execution option for the scopes and results.
type BlockNodeResultMode int

const (
	// ReturnsScope is option to create new scope for the execution of a block
	// and returning it as a Value.
	ReturnsScope BlockNodeResultMode = iota
	// DiscardResults is option to execute block in the context of the calling
	// scope (variables set will go into the invoking scope) and return an
	// empty Value.
	DiscardsResult
)

// BlockNode represents a block in the AST.
type BlockNode struct {
	// ResultMode sets execution option for handling scope and results.
	ResultMode BlockNodeResultMode
	// BeginToken corresponds to "{" token of this block.
	BeginToken syntax.Token
	// End is the end token of this block.
	End EndNode
	// Statements is the list of statements in this block.
	Statements []Node
}

func (n *BlockNode) appendStatement(s Node) {
	n.Statements = append(n.Statements, s)
}

// LocationRange returns the location range for this node.
func (n *BlockNode) LocationRange() syntax.LocationRange {
	if n.BeginToken.TokenType() == syntax.TokenInvalid {
		return syntax.LocationRange{}
	}
	return n.BeginToken.Range().Union(n.End.Value.Range())
}

// ConditionNode represents a conditional in the AST.
type ConditionNode struct {
	// IfToken represents the if token this node starts at.
	IfToken syntax.Token
	// Condition represents the conditional for this node.
	Condition Node
	// IfTrue is the block to be executed if the conditional evaluates to true.
	IfTrue *BlockNode
	// IfFalse should be a *BlockNode or *ConditionNode to represent either the "else" block or "else if" condition, respectively.
	IfFalse Node
}

func (n *ConditionNode) LocationRange() syntax.LocationRange {
	if n.IfFalse != nil {
		return n.IfToken.Range().Union(n.IfFalse.LocationRange())
	}
	return n.IfToken.Range().Union(n.IfTrue.LocationRange())
}

// FunctionCallNode represents a function call in the AST.
type FunctionCallNode struct {
	Function syntax.Token
	Args     *ListNode
	Block    *BlockNode
}

// LocationRange returns the location range for this node.
func (n *FunctionCallNode) LocationRange() syntax.LocationRange {
	if n.Function.TokenType() == syntax.TokenInvalid {
		return syntax.LocationRange{}
	}
	if n.Block != nil {
		return n.Function.Range().Union(n.Block.LocationRange())
	}
	if n.Args != nil {
		return n.Function.Range().Union(n.Args.LocationRange())
	}
	return n.Function.Range()
}

// IdentifierNode represents an identifier in the AST.
type IdentifierNode struct {
	Value syntax.Token
}

// LocationRange returns the location range for this node.
func (n *IdentifierNode) LocationRange() syntax.LocationRange {
	return n.Value.Range()
}

// ListNode represents a list in the AST.
type ListNode struct {
	BeginToken syntax.Token
	End        EndNode
	Contents   []Node
}

// LocationRange returns the location range for this node.
func (n *ListNode) LocationRange() syntax.LocationRange {
	return n.BeginToken.Range().Union(n.End.Value.Range())
}

func (n *ListNode) appendItem(s Node) {
	n.Contents = append(n.Contents, s)
}

// LiteralNode represents a literal in the AST.
type LiteralNode struct {
	// Token is the token for this literal.
	Token syntax.Token
}

// LocationRange returns the location range for this node.
func (n *LiteralNode) LocationRange() syntax.LocationRange {
	return n.Token.Range()
}

// UnaryOpNode represents a unary operation in the AST.
type UnaryOpNode struct {
	// Op is the operator token.
	Op syntax.Token
	// Operand represents the operand of the operation.
	Operand Node
}

// LocationRange returns the location range for this node.
func (n *UnaryOpNode) LocationRange() syntax.LocationRange {
	return n.Op.Range().Union(n.Operand.LocationRange())
}

type BinaryOpNode struct {
	Op    syntax.Token
	Left  Node
	Right Node
}

func (n *BinaryOpNode) LocationRange() syntax.LocationRange {
	return n.Left.LocationRange().Union(n.Right.LocationRange())
}

// BlockCommentNode represents standalone comments (that is, those not
// specifically attached to another syntax element. The most common of these
// is a standard header block. This node contains only the last line of such
// a comment block as the anchor, and other lines of the block comment are
// hung off of it as Before comments, similar to other syntax elements.
type BlockCommentNode struct {
	Comment syntax.Token
}

// LocationRange returns the location range for this node.
func (n *BlockCommentNode) LocationRange() syntax.LocationRange {
	return n.Comment.Range()
}

// EndNode is used as the end object for lists and blocks (rather than
// just the end ']', '}', or ')' syntax.Token). This is so that during formatting
// traversal there is a node that appears at the end of the block to which
// comments can be attached.
type EndNode struct {
	Value syntax.Token
}
