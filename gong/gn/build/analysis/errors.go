// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

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

// OutsideBuildConfigError is returned by functions that expect to only be called from build config.
type OutsideBuildConfigError struct {
	resolve.OriginFunction
}

// Error returns the error string.
func (e OutsideBuildConfigError) Error() string {
	return fmt.Sprintf("called %q outside of build config", e.Call.Function.Value())
}

// Message returns the user-facing error message.
func (OutsideBuildConfigError) Message() string {
	return "Must be called from build config."
}

// HelpText returns the user-facing error help text.
func (e OutsideBuildConfigError) HelpText() string {
	return fmt.Sprintf("%s can only be called from the build configuration file.", e.Call.Function.Value())
}

// ItemInBuildConfigError is returned when attempting to define an item in build config.
type ItemInBuildConfigError struct {
	resolve.OriginFunction
}

// Error returns the error string.
func (e ItemInBuildConfigError) Error() string {
	return fmt.Sprintf("called %q inside of build config", e.Call.Function.Value())
}

// Message returns the user-facing error message.
func (ItemInBuildConfigError) Message() string {
	return "Not valid from the build config."
}

// HelpText returns the user-facing error help text.
func (e ItemInBuildConfigError) HelpText() string {
	return `You can't do this kind of thing from the build config script, silly!
Put it in a regular BUILD file.`
}
