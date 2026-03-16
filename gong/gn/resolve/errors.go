// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"errors"
	"fmt"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
	"go.chromium.org/build/gong/ui"
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

// UnknownFunctionError is returned when referencing an unknown function.
type UnknownFunctionError struct {
	syntax.OriginToken
}

// Error returns the error message.
func (e UnknownFunctionError) Error() string {
	return fmt.Sprintf("unknown function %q", e.Token.Value())
}

// Message returns the user-facing error message.
// The function name is not included because the UI will show the origin source snippet.
func (e UnknownFunctionError) Message() string { return "Unknown function." }

// HelpText returns the user-facing error help text.
// It returns an empty string because there is no detailed help text for this error.
func (e UnknownFunctionError) HelpText() string { return "" }

// ListRemoveNotFoundError is returned when attempting to remove an item that is not in the list.
type ListRemoveNotFoundError struct {
	OriginValue
}

// Error returns the error message.
func (e ListRemoveNotFoundError) Error() string {
	return fmt.Sprintf("item %v not found in list", e.Value)
}

// Message returns the user-facing error message.
func (e ListRemoveNotFoundError) Message() string { return "Item not found" }

// HelpText returns the user-facing error help text.
func (e ListRemoveNotFoundError) HelpText() string {
	return fmt.Sprintf(`You were trying to remove %s
from the list but it wasn't there.`, GNLiteralRvalue(e.Value))
}

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

// KeyError is returned when a member access on a scope fails.
type KeyError struct {
	baseName string
	member   string
	// memberRange is the range of the member access.
	// This struct doesn't take the member node itself because it may not be the desired range.
	//
	// For example:
	//
	//	ERROR at //BUILD.gn:8:9: No value named "example" in scope "foo"
	//	print(foo["example"])
	//	          ^--------
	//	ERROR at //BUILD.gn:8:10: No value named "example" in scope "foo"
	//	print(foo.example)
	//	          ^------
	//
	// Notice how the error excludes the quotes in the former example.
	memberRange syntax.LocationRange
}

// Error returns the error message.
func (e KeyError) Error() string {
	return fmt.Sprintf("key %q not found in %q", e.member, e.baseName)
}

// Message returns the user-facing error message.
func (e KeyError) Message() string {
	return fmt.Sprintf("No value named %q in scope %q", e.member, e.baseName)
}

// HelpText returns the user-facing error help text.
// It returns an empty string because there is no detailed help text for this error.
func (e KeyError) HelpText() string { return "" }

// Location returns the location of the failed member access.
func (e KeyError) Location() syntax.Location {
	return e.memberRange.Begin()
}

// Range returns the range of the failed member access.
func (e KeyError) Range() syntax.LocationRange {
	return e.memberRange
}

// SubscriptError is returned when a subscript access is invalid.
type SubscriptError struct {
	parse.OriginNode
	index int64 // GN numbers are int64
	len   int   // lists are implemented as Go slices where len() is int
}

// Error returns the error message.
func (e SubscriptError) Error() string {
	if e.index < 0 {
		return fmt.Sprintf("negative subscript %d", e.index)
	}
	return fmt.Sprintf("subscript %d out of range for len %d", e.index, e.len)
}

// Message returns the user-facing error message.
func (e SubscriptError) Message() string {
	if e.index < 0 {
		return "Negative array subscript."
	}
	return "Array subscript out of range."
}

// HelpText returns the user-facing error help text.
func (e SubscriptError) HelpText() string {
	if e.index < 0 {
		return fmt.Sprintf("You gave me %d.", e.index)
	} else if e.len == 0 {
		return fmt.Sprintf("You gave me %d but the array has no elements.", e.index)
	}
	return fmt.Sprintf("You gave me %d but I was expecting something from 0 to %d, inclusive.", e.index, e.len-1)
}

// UnusedVarError is returned when a variable is set but not used.
type UnusedVarError struct {
	ident        string
	assignOrigin syntax.LocationRange
}

// Error returns the error message.
func (e UnusedVarError) Error() string {
	return fmt.Sprintf("unused variable: %s", e.ident)
}

// Message returns the user-facing error message.
func (e UnusedVarError) Message() string { return "Assignment had no effect." }

// HelpText returns the user-facing error help text.
func (e UnusedVarError) HelpText() string {
	return fmt.Sprintf("You set the variable %q here and it was unused before it went out of scope.", e.ident)
}

// Location returns the location of the failed member access.
func (e UnusedVarError) Location() syntax.Location {
	return e.assignOrigin.Begin()
}

// Range returns the range of the failed member access.
func (e UnusedVarError) Range() syntax.LocationRange {
	return e.assignOrigin
}

// FloatingScopeError is returned when a free-floating scope is found.
type FloatingScopeError struct {
	parse.OriginNode
}

// Error returns the error message.
func (e FloatingScopeError) Error() string { return "free-floating scopes not permitted" }

// Message returns the user-facing error message.
func (e FloatingScopeError) Message() string { return "This statement has no effect." }

// HelpText returns the user-facing error help text.
func (e FloatingScopeError) HelpText() string {
	return "Either delete it or do something with the result."
}

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

// ValueError is returned when a function is called with an argument of the right type
// but an inappropriate value.
type ValueError struct {
	OriginFunction
	Msg  string
	Help string
}

// Error implements error.
func (e ValueError) Error() string { return fmt.Sprintf("value error: %s", e.Msg) }

// Message returns the user-facing error message.
func (e ValueError) Message() string { return e.Msg }

// HelpText returns the user-facing help text.
func (e ValueError) HelpText() string { return e.Help }

// ScopeMergeError is returned when a scope merge fails due to a value collision.
type ScopeMergeError struct {
	mergeOptions  ScopeMergeOptions
	existingIdent string
}

// Error returns the error message.
func (e ScopeMergeError) Error() string {
	return fmt.Sprintf("scope merge failed: %q exists in destination scope", e.existingIdent)
}

// Message returns the user-facing error message.
func (e ScopeMergeError) Message() string { return "Value collision." }

// HelpText returns the user-facing error help text.
func (e ScopeMergeError) HelpText() string {
	// TODO: add extra help text that points to what's being clobbered.
	return fmt.Sprintf("This %s contains %q", e.mergeOptions.SourceFriendlyName, e.existingIdent)
}

// Location implements PresentableSourceError.
func (e ScopeMergeError) Location() syntax.Location {
	if e.mergeOptions.SourceNode == nil {
		return syntax.Location{}
	}
	return e.mergeOptions.SourceNode.LocationRange().Begin()
}

// Ranges implements PresentableSourceError.
func (e ScopeMergeError) Ranges() []syntax.LocationRange {
	if e.mergeOptions.SourceNode == nil {
		return nil
	}
	return []syntax.LocationRange{e.mergeOptions.SourceNode.LocationRange()}
}

// IntegerLiteralError is returned when an integer literal is invalid.
type IntegerLiteralError struct {
	parse.OriginNode
	message string
}

// Error returns the error message.
func (e IntegerLiteralError) Error() string { return fmt.Sprintf("integer literal err: %s", e.message) }

// Message returns the user-facing error message.
func (e IntegerLiteralError) Message() string { return e.message }

// HelpText returns the user-facing error help text.
func (e IntegerLiteralError) HelpText() string { return "" }

// StringLiteralError is returned when a string literal could not be expanded.
type StringLiteralError struct {
	parse.OriginNode
	message  string
	helpText string
}

// Error returns the error message.
func (e StringLiteralError) Error() string { return fmt.Sprintf("string literal err: %s", e.message) }

// Message returns the user-facing error message.
func (e StringLiteralError) Message() string { return e.message }

// HelpText returns the user-facing error help text.
func (e StringLiteralError) HelpText() string { return e.helpText }

// StringLiteralExpressionError is returned when a string literal could not be expanded
// because it contained an expression interpolation, which failed to be parsed.
type StringLiteralExpressionError struct {
	syntax.OriginToken
	err error
}

// Error returns the error message.
func (e StringLiteralExpressionError) Error() string {
	return fmt.Sprintf("expr in string literal failed to parse: %v", e.err)
}

// Message returns the user-facing error message.
func (e StringLiteralExpressionError) Message() string {
	// Generally string interpolations aren't complex. So the user should just be presented with
	// a single error in the UI that points at the origin syntax token and the error message.
	var gnErr ui.PresentableError
	if errors.As(e.err, &gnErr) {
		return gnErr.Message()
	}
	// However, if the underlying error was an external Go error, we'll expose it in Unwrap.
	// So just show a simple title, then let the UI handle how to render the external error.
	return "String interpolation failed."
}

// HelpText returns the user-facing error help text.
func (e StringLiteralExpressionError) HelpText() string {
	// See the Message function for why we do this.
	var gnErr ui.PresentableError
	if errors.As(e.err, &gnErr) {
		return gnErr.HelpText()
	}
	// If the underlying error was an external Go error, we'll expose it in Unwrap.
	// So don't show any help text in this error, then let the UI handle the unwrapped error.
	return ""
}

// Unwrap returns an underlying error if applicable.
func (e StringLiteralExpressionError) Unwrap() error {
	// Generally string interpolations aren't complex, so the user should not be presented with
	// the underlying GN error, instead we directly consume the error.
	var gnErr ui.PresentableError
	if errors.As(e.err, &gnErr) {
		return nil
	}
	// However if it was an external Go error, then it should be exposed.
	return e.err
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

// OriginValue is an embeddable struct for errors to provide location data for a value.
type OriginValue struct {
	Value Value
}

// Location implements PresentableSourceError.
func (e OriginValue) Location() syntax.Location {
	if e.Value == nil {
		return syntax.Location{}
	}
	return e.Value.OriginNode().LocationRange().Begin()
}

// Ranges implements PresentableSourceError.
func (e OriginValue) Ranges() []syntax.LocationRange {
	if e.Value == nil {
		return nil
	}
	return []syntax.LocationRange{e.Value.OriginNode().LocationRange()}
}
