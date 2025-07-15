// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package syntax

// ErrKind is an enum, with each entry corresponding to a category of errors.
// We use strings rather than ints to make test results more readable.
type ErrKind string

const (
	// Never set this. This is returned by e.Kind() on nil.
	ErrNone ErrKind = "nil"
	// Never set this. This is returned by e.Kind() on a non-syntax-error
	ErrNotSyntaxError ErrKind = "non-syntax-error"
	// TODO(b/388723392): Remove this once it's no longer used.
	ErrUnknown        ErrKind = "ErrUnknown"
	ErrNotImplemented ErrKind = "ErrNotImplemented"
	// go/keep-sorted start
	ErrEOF                     ErrKind = "ErrEOF"
	ErrFileLoadFail            ErrKind = "ErrFileLoadFail"
	ErrInvalidToken            ErrKind = "ErrInvalidToken"
	ErrNewlineInStringConstant ErrKind = "ErrNewlineInStringConstant"
	ErrNotNumeric              ErrKind = "ErrNotNumeric"
	ErrTypeMismatch            ErrKind = "ErrTypeMismatch"
	ErrUnexpectedToken         ErrKind = "ErrUnexpectedToken"
	ErrUnterminatedString      ErrKind = "ErrUnterminatedString"
	ErrUselessAssignment       ErrKind = "ErrUselessAssignment"
	// go/keep-sorted end
)
