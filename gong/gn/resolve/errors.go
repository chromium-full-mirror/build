// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"fmt"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

// ASTError is returned when the resolver encounters a malformed AST.
// This is an internal error that should not be thrown by consumer packages.
//
// C++ GN uses DCHECK to fail when it encounters a malformed AST, as it is an internal error
// that should not be encountered if the parser is working correctly.
//
// We prefer to return a concrete type instead of panicking.
type ASTError struct {
	parse.OriginNode
	details string
}

// Error implements PresentableError.
func (e ASTError) Error() string {
	return fmt.Sprintf("got invalid AST: %s", e.details)
}

// Message implements PresentableError.
func (e ASTError) Message() string { return "Invalid AST" }

// HelpText implements PresentableError.
func (e ASTError) HelpText() string { return e.details }

// UnimplementedNodeError is returned when attempting to resolve a node that we don't support yet.
type UnimplementedNodeError struct {
	parse.OriginNode
	details string
}

// Error implements PresentableError.
func (e UnimplementedNodeError) Error() string {
	return fmt.Sprintf("unimplemented node %T(%v)", e.OriginNode, e.OriginNode)
}

// Message implements PresentableError.
func (e UnimplementedNodeError) Message() string { return e.Error() }

// HelpText implements PresentableError.
func (e UnimplementedNodeError) HelpText() string { return e.details }

// ArgumentCountError is returned when a function call has the wrong number of arguments.
//
// TODO: Maybe we can standardize GN argument count error messages by taking the expected and actual counts here?
type ArgumentCountError struct {
	OriginFunction
	Msg  string
	Help string
}

// Error implements PresentableError.
func (e ArgumentCountError) Error() string {
	return fmt.Sprintf("argument count error: %s", e.Msg)
}

// Message implements PresentableError.
func (e ArgumentCountError) Message() string { return e.Msg }

// HelpText implements PresentableError.
func (e ArgumentCountError) HelpText() string { return e.Help }

// AssertError is returned when AssertFunction fails an assertion.
type AssertError struct {
	OriginFunction
	Details string
}

// Error implements PresentableError.
func (e AssertError) Error() string { return fmt.Sprintf("assert error: %s", e.Details) }

// Message implements PresentableError.
func (AssertError) Message() string { return "Assertion failed." }

// HelpText returns the assertion details as the help text for the assert failure, if provided.
func (e AssertError) HelpText() string { return e.Details }

// OriginFunction is an embeddable struct for errors to provide location data for a function call.
type OriginFunction struct {
	Call *parse.FunctionCallNode
}

// Location implements PresentableSourceError.
func (e OriginFunction) Location() syntax.Location {
	if e.Call == nil {
		return syntax.Location{}
	}
	return e.Call.LocationRange().Begin()
}

// Ranges implements PresentableSourceError.
func (e OriginFunction) Ranges() []syntax.LocationRange {
	if e.Call == nil {
		return nil
	}
	return []syntax.LocationRange{e.Call.LocationRange()}
}
