// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package parse

import (
	"fmt"

	"go.chromium.org/build/gong/gn/syntax"
)

// OriginNode is an embeddable struct for errors to provide location data originating from a Node.
type OriginNode struct {
	Node Node
}

// Location implements PresentableSourceError.
func (e OriginNode) Location() syntax.Location {
	if e.Node == nil {
		return syntax.Location{}
	}
	return e.Node.LocationRange().Begin()
}

// Ranges implements PresentableSourceError.
func (e OriginNode) Ranges() []syntax.LocationRange {
	if e.Node == nil {
		return nil
	}
	return []syntax.LocationRange{e.Node.LocationRange()}
}

// NodeError represents a parse error associated with a node.
type NodeError struct {
	OriginNode
	message  string
	helpText string
}

// Error implements PresentableError.
func (e NodeError) Error() string { return fmt.Sprintf("parse error: %s", e.message) }

// Message implements PresentableError.
func (e NodeError) Message() string { return e.message }

// HelpText implements PresentableError.
func (e NodeError) HelpText() string { return e.helpText }

// TokenError represents a parse error associated with a token.
type TokenError struct {
	syntax.OriginToken
	message  string
	helpText string
}

// Error implements PresentableError.
func (e TokenError) Error() string { return fmt.Sprintf("parse error: %s", e.message) }

// Message implements PresentableError.
func (e TokenError) Message() string { return e.message }

// HelpText implements PresentableError.
func (e TokenError) HelpText() string { return e.helpText }

// EOF represents an end of file error.
type EOF struct {
	token syntax.Token
}

// Error returns EOF like io.EOF.
func (e EOF) Error() string { return "EOF" }

// Message returns the GN user-facing message.
func (e EOF) Message() string { return "Reached end of file during parsing" }

// HelpText returns blank, as there is no detailed help text for EOF.
func (e EOF) HelpText() string { return "" }

// Location returns the location of the parser when EOF was reached.
func (e EOF) Location() syntax.Location {
	return e.token.Range().Begin()
}

// Ranges returns the range of the parser when EOF was reached.
func (e EOF) Ranges() []syntax.LocationRange {
	return []syntax.LocationRange{e.token.Range()}
}
