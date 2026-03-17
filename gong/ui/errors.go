// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ui

import (
	"go.chromium.org/build/gong/gn/syntax"
)

// A PresentableError is a GN error that provides a user-facing message about
// the error.
//
// It may also indicate sub errors by following Go 1.20 error wrapping
// conventions, in other words it implements one of:
//
//	Unwrap() error
//	Unwrap() []error
//
// If e.Unwrap() returns a non-nil PresentableError or a slice containing one
// or more PresentableErrors, then these will be presented as sub errors.
//
// Because this follows standard Go error wrapping conventions, the error tree
// may be examined using the errors package.
type PresentableError interface {
	// Message returns a user-facing description of the error.
	Message() string
	// HelpText returns detailed user-facing help text for the error if available.
	HelpText() string
	// Error returns an internal-facing error message for compatibility with
	// standard Go error handling.
	//
	// Implementers that don't want to maintain both separate user-facing and
	// internal-facing error messages are suggested to return Message() but
	// prefixed with some context e.g. errors returned by the syntax package
	// could be prefixed with "syntax error: ".
	Error() string
}

// A PresentableSourceError is a [PresentableError] that may return an origin location and range(s).
type PresentableSourceError interface {
	PresentableError
	// Location returns origin location of the error if available.
	// The zero value will be treated as an unset location.
	Location() syntax.Location
	// Ranges returns origin range(s) of the error if available.
	Ranges() []syntax.LocationRange
}

// A StackTraceError is a [PresentableError] that unwraps with the root cause as innermost,
// but for rendering the UI should display the root cause first.
//
// For example, consider an error from an import several layers deep.
//
// In Go, the error message would follow the logical order of a "DoImport" returning an
// error, then the "DoImport" calling it wrapping that error, and so on until we hit
// the top-level exec call:
//
//	import //foo.gni failed: import //bar.gni failed: import //baz.gni failed: undefined identifier "give_you_up"
//
// However, this is reversed for UI error-reporting purposes.
//
// The root cause should be displayed first, then the chain of errors in stack trace order:
//
//	ERROR at //baz.gni:5:15: Undefined identifier.
//	never_going_to = give_you_up
//	                 ^----------
//	See //baz.gni:2:1: whence it was imported.
//	import("//baz.gni")
//	^-----------------
//	See //foo.gni:2:1: whence it was imported.
//	import("//bar.gni")
//	^-----------------
//	See //BUILD.gn:2:1: whence it was imported.
//	import("//foo.gni")
//	^-----------------
//
// Implementers of this interface are expected to return the chain of errors in stack order,
// that is, the root cause first, then the reverse chain of wrapped orders.
// The error itself should not be included in the stack.
type StackTraceError interface {
	PresentableError
	// Stack returns the chain of wrapped errors in stack trace order.
	// The first element is the root cause, and the last element is the error before this error.
	// This error itself is not included in the stack.
	//
	// Use Unwrap() if you want to iterate through the errors in the order they were wrapped.
	Stack() []error
}
