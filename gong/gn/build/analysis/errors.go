// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"fmt"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/parse"
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

// ItemRedefinedError is returned when encountering a second attempt to define
// the same item.
type ItemRedefinedError struct {
	previousOrigin parse.Node
	duplicateItem  Item
}

// Error returns the error string.
func (e ItemRedefinedError) Error() string {
	return fmt.Sprintf("redefined item: %s", e.duplicateItem.Label().UserVisibleString(true))
}

// Message returns the user-facing error message.
func (ItemRedefinedError) Message() string { return "Duplicate definition." }

// HelpText returns the user-facing error help text.
func (e ItemRedefinedError) HelpText() string {
	// TODO: need to implement ShouldShowToolchain for parity with C++ GN
	// (i.e. don't need to always show the toolchain)
	return fmt.Sprintf(
		`The item
  %s
was already defined.`,
		e.duplicateItem.Label().UserVisibleString(true))
}

type itemRedefinedSuberror struct {
	parse.OriginNode
}

func (e itemRedefinedSuberror) Error() string  { return fmt.Sprintf("previously saw item: %v", e.Node) }
func (itemRedefinedSuberror) Title() string    { return "Previous definition:" }
func (itemRedefinedSuberror) HelpText() string { return "" }

// Unwrap returns a single suberror to indicate where the previous definition of the item was seen.
func (e ItemRedefinedError) Unwrap() error {
	return itemRedefinedSuberror{OriginNode: parse.OriginNode{Node: e.previousOrigin}}
}

// ItemTypeMismatchError is returned when encountering a reference to an item that
// doesn't match the type it was defined as.
//
// For example, a config() object being referenced from deps of a target or
// vice-versa.
type ItemTypeMismatchError struct {
	parse.OriginNode
	label             environment.Label
	itemOrPlaceholder Item
	existingRecord    *builderRecord
}

// Error returns the error string.
func (e ItemTypeMismatchError) Error() string {
	return fmt.Sprintf("tried to reference item as a %T, but was previously seen as a %T",
		e.itemOrPlaceholder, e.existingRecord.item)
}

// Message returns the user-facing error message.
func (ItemTypeMismatchError) Message() string { return "Item type does not match." }

// HelpText returns the user-facing error help text.
func (e ItemTypeMismatchError) HelpText() string {
	return fmt.Sprintf(
		`The type of %s here is a %s type but was previously seen as a %s type.

The most common cause is that the label of a config was put
in the deps section of a target (or vice-versa).`,
		e.label.UserVisibleString(true),
		itemTypeName(e.itemOrPlaceholder),
		itemTypeName(e.existingRecord.item))
}

func itemTypeName(item Item) string {
	switch item.(type) {
	case *Target:
		return "target"
	case *Config:
		return "config"
	case *Toolchain:
		return "toolchain"
	case *Pool:
		return "pool"
	}
	return "unknown"
}

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
