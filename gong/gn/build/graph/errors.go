// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"fmt"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

// ToolError is returned when there is an error in a tool definition.
type ToolError struct {
	parse.OriginNode
	message  string
	helpText string
}

// Error returns the error string.
func (e ToolError) Error() string {
	return fmt.Sprintf("error defining tool: %s", e.message)
}

// Message returns the user-facing error message.
func (e ToolError) Message() string { return e.message }

// HelpText returns the user-facing error help text.
func (e ToolError) HelpText() string { return e.helpText }

// ToolOutsideToolchain is returned when attempting to define a tool outside of a toolchain.
type ToolOutsideToolchain struct {
	resolve.OriginFunction
}

// Error returns the error string.
func (ToolOutsideToolchain) Error() string { return "called tool outside of toolchain" }

// Message returns the user-facing error message.
func (ToolOutsideToolchain) Message() string { return "tool() called outside of toolchain()." }

// HelpText returns the user-facing error help text.
func (ToolOutsideToolchain) HelpText() string {
	return "The tool() function can only be used inside a toolchain() definition."
}

// SubstitutionFormatError is returned when a substitution pattern is malformed.
type SubstitutionFormatError struct {
	invalidPart string
}

// Error returns the error string.
func (e SubstitutionFormatError) Error() string {
	return fmt.Sprintf("unknown substitution type: %s", e.invalidPart)
}

// Message returns the user-facing error message.
func (e SubstitutionFormatError) Message() string { return "Invalid substitution type." }

// HelpText returns the user-facing error help text.
func (e SubstitutionFormatError) HelpText() string {
	return fmt.Sprintf("Don't recognize the substitution pattern starting with %q.", e.invalidPart)
}

// FrameworkMissingExtension is returned when a framework input is invalid.
type FrameworkMissingExtension struct {
	resolve.OriginValue
	framework string
}

// Error returns the error string.
func (e FrameworkMissingExtension) Error() string {
	return fmt.Sprintf("framework missing extension: %q", e.framework)
}

// Message returns the user-facing error message.
func (e FrameworkMissingExtension) Message() string {
	return `This frameworks value is wrong. All listed frameworks names must not include any
path component and have ".framework" extension.`
}

// HelpText returns the user-facing error help text.
// It returns an empty string because there is no detailed help text for this error.
func (FrameworkMissingExtension) HelpText() string { return "" }
