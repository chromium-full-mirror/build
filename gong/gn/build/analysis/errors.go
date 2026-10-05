// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"errors"
	"fmt"
	"slices"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
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
	duplicateItem  graph.Item
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
	return itemRedefinedSuberror{Node: e.previousOrigin}
}

// ItemTypeMismatchError is returned when encountering a reference to an item that
// doesn't match the type it was defined as.
//
// For example, a config() object being referenced from deps of a target or
// vice-versa.
type ItemTypeMismatchError struct {
	parse.OriginNode
	label             environment.Label
	itemOrPlaceholder graph.Item
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

// TODO: make this a function on graph.Item rather than this package?
func itemTypeName(item graph.Item) string {
	switch item.(type) {
	case *graph.Target:
		return "target"
	case *graph.Config:
		return "config"
	case *graph.Toolchain:
		return "toolchain"
	case *graph.Pool:
		return "pool"
	}
	return "unknown"
}

// ImportError is returned when an import call fails while evaluating the imported file.
type ImportError struct {
	parse.OriginNode
	file  fs.SourceFile
	stack []error
}

// makeImportError wraps an error in an ImportError, preserving the unwrap chain if the cause is an ImportError.
func makeImportError(nodeForErr parse.Node, file fs.SourceFile, err error) *ImportError {
	ret := &ImportError{Node: nodeForErr, file: file}
	if e, ok := errors.AsType[*ImportError](err); ok {
		// Don't mutate the slice held by the wrapped ImportError.
		ret.stack = slices.Concat(e.stack, []error{err})
	} else {
		ret.stack = []error{err}
	}
	return ret
}

// Stack returns the chain of wrapped errors in stack trace order.
func (e ImportError) Stack() []error {
	return e.stack
}

// Error returns the error string.
func (e ImportError) Error() string {
	if len(e.stack) == 0 {
		return fmt.Sprintf("import %s failed, but cause missing", e.file.Filename())
	}
	return fmt.Sprintf("import %s failed: %v", e.file.Filename(), e.stack[0])
}

// Message returns the user-facing error message.
//
// Because ui.StackTraceError is used, this function is expected to be
// called *after* the root error has been displayed.
//
// This should result in something like:
//
//	ERROR at //baz.gni:5:15: Undefined identifier.
//	never_going_to = give_you_up
//	                 ^----------
//	See //baz.gni:2:1: whence it was imported.
//	import("//baz.gni")
//	^-----------------
//	See //foo.gni:2:1: whence it was imported.
//	import("//bar.gni")
//	^-----------------
//	See //BUILD.gn:2:1: whence it was imported.
//	import("//foo.gni")
//	^-----------------
//
// In case an ImportError is accidentally constructed without an underlying
// cause, this is handled so that a fallback is displayed:
//
//	ERROR at //BUILD.gn:2:1: Import failed, but cause missing.
//	import("//foo.gni")
//	^-----------------
func (e ImportError) Message() string {
	if len(e.stack) == 0 {
		return "Import failed, but cause missing."
	}
	return "whence it was imported."
}

// HelpText returns the user-facing error help text.
// It returns an empty string because there is no detailed help text for this error.
func (e ImportError) HelpText() string { return "" }

// ImportLoopError is returned when an import call creates a circular dependency.
type ImportLoopError struct {
	parse.OriginNode
	cause fs.SourceFile
	chain []fs.SourceFile
}

// Error returns the error string.
func (e ImportLoopError) Error() string {
	return fmt.Sprintf("%s is part of an import loop", e.cause.Filename())
}

// Message returns the user-facing error message.
func (e ImportLoopError) Message() string {
	return fmt.Sprintf("%s is part of an import loop.", e.cause.Filename())
}

// HelpText returns the user-facing error help text.
// It returns an empty string because there is no detailed help text for this error.
func (e ImportLoopError) HelpText() string { return "" }
