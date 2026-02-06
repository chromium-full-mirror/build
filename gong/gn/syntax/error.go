// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package syntax

import (
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
