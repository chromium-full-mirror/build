// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package fs

import (
	"fmt"

	"go.chromium.org/build/gong/gn/syntax"
)

// LoadError is returned when a file can't be loaded.
type LoadError struct {
	origin syntax.LocationRange
	path   string
	// TODO: should we implement Unwrap and return the underlying go error?
	// the problem with doing so right now is ui package will always print all wrapped errors
	// but we don't want to print the underlying go error here.
	err error
	// The secondary path that was attempted, if any.
	secondaryPath string
	secondaryErr  error
}

// Error returns the error string.
func (e LoadError) Error() string {
	if e.secondaryPath != "" {
		return fmt.Sprintf("failed to load %q: %v, also tried %q: %v", e.path, e.err, e.secondaryPath, e.secondaryErr)
	}
	return fmt.Sprintf("failed to load %q: %v", e.path, e.err)
}

// Message returns the user-facing error message.
func (e LoadError) Message() string {
	if e.secondaryPath != "" {
		return "Can't load input file."
	}
	return fmt.Sprintf("Unable to load %q.", e.path)
}

// HelpText returns the user-facing help text.
func (e LoadError) HelpText() string {
	if e.secondaryPath != "" {
		// NOTE: The quoting behavior is inconsistent with above but is
		// consistent with C++ GN. See:
		// https://source.chromium.org/gn/gn/+/main:src/gn/input_file_manager.cc;l=58-75;drc=8bd36a27c0764c869d40ac4102a24720b781b389
		return fmt.Sprintf(
			`Unable to load:
  %s
I also checked in the secondary tree for:
  %s`,
			e.path,
			e.secondaryPath)
	}
	return ""
}

// Location returns the source location of the load attempt.
func (e LoadError) Location() syntax.Location {
	return e.origin.Begin()
}

// Ranges returns the source range of the load attempt.
func (e LoadError) Ranges() []syntax.LocationRange {
	return []syntax.LocationRange{e.origin}
}
