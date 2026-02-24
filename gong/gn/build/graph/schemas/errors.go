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
