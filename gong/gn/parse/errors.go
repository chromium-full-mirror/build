// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package parse

import (
	"fmt"

	"go.chromium.org/build/gong/gn/syntax"
)

// NodeError represents a parse error associated with a node.
type NodeError struct {
	node     Node
	message  string
	helpText string
}

// Error implements PresentableError.
func (e NodeError) Error() string { return fmt.Sprintf("parse error: %s", e.message) }

// Message implements PresentableError.
func (e NodeError) Message() string { return e.message }

// HelpText implements PresentableError.
func (e NodeError) HelpText() string { return e.helpText }

// Location implements PresentableSourceError.
func (e NodeError) Location() syntax.Location {
	if e.node == nil {
		return syntax.Location{}
	}
	return e.node.LocationRange().Begin()
}

// Ranges implements PresentableSourceError.
func (e NodeError) Ranges() []syntax.LocationRange {
	if e.node == nil {
		return nil
	}
	return []syntax.LocationRange{e.node.LocationRange()}
}

// TokenError represents a parse error associated with a token.
type TokenError struct {
	token    syntax.Token
	message  string
	helpText string
}

// Error implements PresentableError.
func (e TokenError) Error() string { return fmt.Sprintf("parse error: %s", e.message) }

// Message implements PresentableError.
func (e TokenError) Message() string { return e.message }

// HelpText implements PresentableError.
func (e TokenError) HelpText() string { return e.helpText }

// Location implements PresentableSourceError.
func (e TokenError) Location() syntax.Location {
	return e.token.Range().Begin()
}

// Ranges implements PresentableSourceError.
func (e TokenError) Ranges() []syntax.LocationRange {
	return []syntax.LocationRange{e.token.Range()}
}

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
