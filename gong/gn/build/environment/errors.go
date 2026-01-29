// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package environment

import (
	"fmt"
)

// BuildConfigError is returned if the build configuration is invalid.
type BuildConfigError struct {
	Msg  string
	Help string
}

// Error returns the golang error string.
func (e BuildConfigError) Error() string {
	return fmt.Sprintf("build config error: %s", e.Msg)
}

// Message returns the user-facing error.
func (e BuildConfigError) Message() string { return e.Msg }

// HelpText returns the user-facing help text.
func (e BuildConfigError) HelpText() string { return e.Help }

// IllegalStateError is returned when the builder is in an illegal state.
// This should only be used to indicate an unexpected state in the builder.
//
// For example, it receives a scope constructed outside of the GN build context.
// This is possible unlike C++ GN, because in C++ GN buildfile concepts are
// part of the scope implementation.
// Here, buildfile-specific concepts are not in the resolve.Scope implementation.
// It's therefore theoretically possible to pass a scope that doesn't have
// any buildfile-specific information.
type IllegalStateError struct {
	Reason string
}

// Error returns the golang error string.
func (e IllegalStateError) Error() string {
	return fmt.Sprintf("illegal state: %s", e.Reason)
}

// Message returns the user-facing error.
func (e IllegalStateError) Message() string { return "Builder in illegal state." }

// HelpText returns the user-facing help text.
func (e IllegalStateError) HelpText() string { return e.Reason }
