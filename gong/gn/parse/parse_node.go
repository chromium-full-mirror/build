// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package parse

import "infra/build/gong/gn/syntax"

// ParseNode is a node in the AST.
type ParseNode interface {
	// LocationRange is the file range this node represents.
	LocationRange() syntax.LocationRange
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
	// Statements is the list of statements in this block.
	Statements []ParseNode
}

// Range returns the location range for this node.
func (n BlockNode) LocationRange() syntax.LocationRange {
	// TODO: implement by checking statements
	return syntax.LocationRange{}
}
