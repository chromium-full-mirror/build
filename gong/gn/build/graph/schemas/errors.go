// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package schemas

import (
	"fmt"

	"go.chromium.org/build/gong/gn/resolve"
)

// NotImplementedError is returned by functions that are missing functionality
// compared to GN.
type NotImplementedError struct {
	resolve.OriginFunction
	what string
}

// Error returns the error string.
func (e NotImplementedError) Error() string {
	return fmt.Sprintf("not implemented: %s", e.what)
}

// Message returns the user-facing error message.
func (NotImplementedError) Message() string {
	return "Not implemented."
}

// HelpText returns the user-facing error help text.
func (e NotImplementedError) HelpText() string {
	return fmt.Sprintf("%s is not implemented.", e.what)
}

// BinaryMixedSourcesError is returned when a binary target has mixed source types.
type BinaryMixedSourcesError struct{}

// Error returns the error string.
func (BinaryMixedSourcesError) Error() string { return "target has mixed languages" }

// Message returns the user-facing error message.
func (BinaryMixedSourcesError) Message() string {
	return "More than one language used in target sources."
}

// HelpText returns the user-facing error help text.
func (BinaryMixedSourcesError) HelpText() string {
	return "Mixed sources are not allowed, unless they are compilation-compatible (e.g. Objective C and C++)."
}

// BinaryInvalidSourceError is returned when a binary target has an invalid source type.
type BinaryInvalidSourceError struct {
	targetName string
	sourceName string
}

// Error returns the error string.
func (e BinaryInvalidSourceError) Error() string {
	return fmt.Sprintf("%s is not a valid source for %s", e.sourceName, e.targetName)
}

// Message returns the user-facing error message.
func (e BinaryInvalidSourceError) Message() string {
	return fmt.Sprintf("Only source, header, and object files belong in the sources of a %s. %s is not one of the valid types.", e.targetName, e.sourceName)
}

// HelpText returns the user-facing error help text.
// It returns an empty string because there is no detailed help text for this error.
func (BinaryInvalidSourceError) HelpText() string { return "" }

// CrateRootNotFoundError is returned when the crate root is not found.
type CrateRootNotFoundError struct {
	expected string
}

// Error returns the error string.
func (CrateRootNotFoundError) Error() string {
	return "crate root not found"
}

// Message returns the user-facing error message.
func (e CrateRootNotFoundError) Message() string {
	return fmt.Sprintf(`Missing "crate_root" and missing %q in sources.`, e.expected)
}

// HelpText returns the user-facing error help text.
// It returns an empty string because there is no detailed help text for this error.
func (CrateRootNotFoundError) HelpText() string { return "" }

// ActionMissingScriptError is returned when an action target is missing a script.
type ActionMissingScriptError struct{}

// Error returns the error string.
func (ActionMissingScriptError) Error() string {
	return "action missing script"
}

// Message returns the user-facing error message.
func (ActionMissingScriptError) Message() string {
	return `This target type requires a "script".`
}

// HelpText returns the user-facing error help text.
func (ActionMissingScriptError) HelpText() string { return "" }
