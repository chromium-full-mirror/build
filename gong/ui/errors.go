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

// A PresentableSourceError is a PresentableError that may return an origin location and range(s).
type PresentableSourceError interface {
	PresentableError
	// Location returns origin location of the error if available.
	// The zero value will be treated as an unset location.
	Location() syntax.Location
	// Ranges returns origin range(s) of the error if available.
	Ranges() []syntax.LocationRange
}
