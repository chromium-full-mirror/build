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
