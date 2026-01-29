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
	// Never set this. This is returned by e.Kind() on nil.
	ErrNone ErrKind = "nil"
	// Never set this. This is returned by e.Kind() on a non-syntax-error
	ErrNotSyntaxError ErrKind = "non-syntax-error"
	// TODO(b/388723392): Remove this once it's no longer used.
	ErrUnknown        ErrKind = "ErrUnknown"
	ErrNotImplemented ErrKind = "ErrNotImplemented"
	// Deprecated: Implement ui.PresentableError instead.
	ErrInvalidFormat ErrKind = "ErrInvalidFormat"
	// Deprecated: Implement ui.PresentableError instead.
	ErrInvalidOperation ErrKind = "ErrInvalidOperation"
)
