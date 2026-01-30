// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package syntax

import (
	"errors"
	"fmt"
)

// OriginToken is an embeddable struct for errors to provide location data originating from a Token.
type OriginToken struct {
	Token Token
}

// Location implements PresentableSourceError.
func (e OriginToken) Location() Location {
	if e.Token == (Token{}) {
		return Location{}
	}
	return e.Token.Range().Begin()
}

// Ranges implements PresentableSourceError.
func (e OriginToken) Ranges() []LocationRange {
	if e.Token == (Token{}) {
		return nil
	}
	return []LocationRange{e.Token.Range()}
}

// IllegalStateError is returned when the tokenizer is in an illegal state.
type IllegalStateError struct {
	err
}

// InvalidTokenError is returned when the tokenizer encounters an invalid token.
type InvalidTokenError struct {
	err
}

// NewlineInStringConstant is returned when a newline is encountered in a string constant.
type NewlineInStringConstant struct {
	err
}

// NonNumericError is returned when a non-numeric value is encountered where a numeric value is expected.
type NonNumericError struct {
	err
}

// UnterminatedStringError is returned when a string constant is not terminated.
type UnterminatedStringError struct {
	err
}

// err is a base struct for syntax errors.
type err struct {
	start    Location
	end      Location
	message  string
	helpText string
}

// Error implements PresentableError.
func (e err) Error() string { return fmt.Sprintf("syntax error: %s", e.message) }

// Message implements PresentableError.
func (e err) Message() string { return e.message }

// HelpText implements PresentableError.
func (e err) HelpText() string { return e.helpText }

// Location implements PresentableSourceError.
func (e err) Location() Location { return e.start }

// Ranges implements PresentableSourceError.
func (e err) Ranges() []LocationRange {
	if e.end == (Location{}) {
		return []LocationRange{}
	}
	return []LocationRange{{begin: e.start, end: e.end}}
}

// Error represents a syntax error.
//
// Deprecated: Implement ui.PresentableError instead.
type Error struct {
	location  Location
	ranges    []LocationRange
	message   string
	helpText  string
	subErrors []error
	kind      ErrKind
}

// MakeErrorAt makes an error at the provided location and ranges.
//
// Deprecated: Implement ui.PresentableError instead.
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
		var syntaxErr Error
		if errors.As(err, &syntaxErr) {
			return syntaxErr.kind
		}
		return ErrNotSyntaxError
	} else {
		return ErrNone
	}
}

// AsErrKind returns the error if it matches the error kind.
// If it doesn't match, returns nil and the actual error kind.
//
// Deprecated: Implement ui.PresentableError and use errors.As?
func AsErrKind(err error, kind ErrKind) (*Error, ErrKind) {
	var syntaxErr Error
	if errors.As(err, &syntaxErr) {
		if syntaxErr.kind == kind {
			return &syntaxErr, syntaxErr.kind
		} else {
			return nil, syntaxErr.kind
		}
	}
	return nil, GetErrKind(err)
}
