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
}

// MakeErrorAt makes an error at the provided location and ranges.
// TODO(b/388723392): just make the struct fields exported?
func MakeErrorAt(location Location, ranges []LocationRange, message, helpText string) Error {
	return Error{
		location: location,
		ranges:   ranges,
		message:  message,
		helpText: helpText,
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
