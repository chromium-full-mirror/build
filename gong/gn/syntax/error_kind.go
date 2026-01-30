// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package syntax

// ErrKind is an enum, with each entry corresponding to a category of errors.
// We use strings rather than ints to make test results more readable.
//
// Deprecated: Implement ui.PresentableError instead.
type ErrKind string

const (
	// Deprecated: Implement ui.PresentableError instead.
	ErrNone ErrKind = "nil"
	// Deprecated: Implement ui.PresentableError instead.
	ErrNotSyntaxError ErrKind = "non-syntax-error"
	// Deprecated: Implement ui.PresentableError instead.
	ErrUnknown ErrKind = "ErrUnknown"
)
