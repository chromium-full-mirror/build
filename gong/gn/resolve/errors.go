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

// UndefinedIdentifierError is returned when referencing an undefined identifier.
type UndefinedIdentifierError struct {
	syntax.OriginToken
}

// Error reports the undefined identifier.
func (e UndefinedIdentifierError) Error() string {
	return fmt.Sprintf("undefined identifier %q", e.Token.Value())
}

// Message returns the GN user-facing message.
// The identifier name is not included because the UI will show the origin source snippet.
func (e UndefinedIdentifierError) Message() string { return "Undefined identifier." }

// HelpText implements PresentableError.
// It returns an empty string because there is no detailed help text for this error.
func (e UndefinedIdentifierError) HelpText() string { return "" }

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

// TypeError is returned when an operation could not be performed due to a type mismatch.
//
// Examples include:
//
//   - Argument type to a function not matching the expected type
//   - Operand type not matching expected type for an operator
type TypeError struct {
	// Value is the value that caused the error.
	Value Value
	// Msg is the user-facing error message.
	Msg string
	// Help is the help text for the error.
	Help string
	// locationOverride is for internal use, allowing override of the location of a type error
	// when it would be more useful to depict the error at a different location.
	//
	// We don't expose this for use outside of this package, because it is only useful for
	// implementing operators, which consumers of this package should not need to do.
	//
	// For example,
	//
	//	invalid = evaluates_to_int || true
	//
	// is a type error, but it would be more useful for the UI to depict the location of the
	// error as:
	//
	//	invalid = evaluates_to_int || true
	//	          ~~~~~~~~~~~~~~~~~^~
	//
	// instead of:
	//
	//	invalid = evaluates_to_int || true
	//	          ^~~~~~~~~~~~~~~~~~~
	locationOverride syntax.Location
	// rangesOverride is for internal use, allowing override of the ranges of a type error
	// when it would be more useful to depict the error at a different range.
	//
	// We don't expose this for use outside of this package, because it is only useful for
	// implementing operators, which consumers of this package should not need to do.
	//
	// For example,
	//
	//	invalid_result = true ||
	//	    evaluates_to_int
	//
	// is a type error, but it would be more useful for the UI to depict the range of the
	// error as:
	//
	//	invalid_result = true ||
	//	                      ^~
	//	    evaluates_to_int
	//	    ~~~~~~~~~~~~~~~~
	//
	// instead of:
	//
	//	invalid_result = true ||
	//	    evaluates_to_int
	//	    ^~~~~~~~~~~~~~~~
	rangesOverride []syntax.LocationRange
}

// Error implements PresentableError.
func (e TypeError) Error() string { return fmt.Sprintf("type error: %s", e.Msg) }

// Message implements PresentableError.
func (e TypeError) Message() string { return e.Msg }

// HelpText implements PresentableError.
func (e TypeError) HelpText() string { return e.Help }

// Location implements PresentableSourceError.
func (e TypeError) Location() syntax.Location {
	if e.locationOverride != (syntax.Location{}) {
		return e.locationOverride
	}
	if e.Value == nil {
		return syntax.Location{}
	}
	return e.Value.OriginNode().LocationRange().Begin()
}

// Ranges implements PresentableSourceError.
func (e TypeError) Ranges() []syntax.LocationRange {
	if e.rangesOverride != nil {
		return e.rangesOverride
	}
	if e.Value == nil {
		return nil
	}
	return []syntax.LocationRange{e.Value.OriginNode().LocationRange()}
}

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
