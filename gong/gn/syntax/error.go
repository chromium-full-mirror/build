// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package syntax

import (
	"fmt"
)

// Error represents a syntax error.
type Error struct {
	location  Location
	ranges    []LocationRange
	message   string
	helpText  string
	subErrors []error
	kind      ErrKind
}

// MakeErrorAt makes an error at the provided location and ranges.
// TODO(b/388723392): just make the struct fields exported?
func MakeErrorAt(location Location, ranges []LocationRange, kind ErrKind, message, helpText string) error {
	return Error{
		location: location,
		ranges:   ranges,
		message:  message,
		helpText: helpText,
		kind:     kind,
	}
}

// Error returns formatted message for this error.
func (e Error) Error() string {
	return fmt.Sprintf("syntax error at %s: %q", e.location.Describe(true), e.message)
}

// Unwrap returns wrapped errors.
func (e Error) Unwrap() []error {
	return e.subErrors
}

// Location returns location this error occurred.
func (e Error) Location() Location {
	return e.location
}

// Ranges returns location ranges this error occurred.
func (e Error) Ranges() []LocationRange {
	return e.ranges
}

// Message returns message for this error.
func (e Error) Message() string {
	return e.message
}

// HelpText returns help text for this error, if available.
func (e Error) HelpText() string {
	return e.helpText
}

// Kind returns error kind for this error
func (e Error) Kind() ErrKind {
	return e.kind
}

// GetErrKind returns the kind of error err corresponds to.
func GetErrKind(err error) ErrKind {
	if err != nil {
		if syntaxErr, ok := err.(Error); ok {
			return syntaxErr.kind
		}
		return ErrNotSyntaxError
	} else {
		return ErrNone
	}
}

// IsErrKind returns whether the err matches error kind.
// If err is not a syntax.Error, returns false.
// Do not use for testing - prefer ExpectErrKind.
func IsErrKind(err error, kind ErrKind) bool {
	if err == nil {
		return false
	}
	if syntaxErr, ok := err.(Error); ok {
		return syntaxErr.kind == kind
	}
	return false
}

// AsErrKind returns the error if it matches the error kind.
// If it doesn't match, returns nil and the actual error kind.
func AsErrKind(err error, kind ErrKind) (*Error, ErrKind) {
	if syntaxErr, ok := err.(Error); ok {
		if syntaxErr.kind == kind {
			return &syntaxErr, syntaxErr.kind
		} else {
			return nil, syntaxErr.kind
		}
	}
	return nil, GetErrKind(err)
}
