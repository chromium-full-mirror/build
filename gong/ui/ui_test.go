// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ui

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/syntax"
)

// mockPresentableError implements PresentableError.
type mockPresentableError struct {
	msg  string
	help string
}

func (e mockPresentableError) Error() string    { return "golang error: " + e.msg }
func (e mockPresentableError) Message() string  { return e.msg }
func (e mockPresentableError) HelpText() string { return e.help }

// mockPresentableSourceError implements PresentableSourceError.
type mockPresentableSourceError struct {
	msg  string
	help string
	loc  syntax.Location
}

func (e mockPresentableSourceError) Error() string                  { return "golang error: " + e.msg }
func (e mockPresentableSourceError) Message() string                { return e.msg }
func (e mockPresentableSourceError) HelpText() string               { return e.help }
func (e mockPresentableSourceError) Location() syntax.Location      { return e.loc }
func (e mockPresentableSourceError) Ranges() []syntax.LocationRange { return nil }

// mockUnwrapError simulates a GN error that implements Unwrap() error.
type mockUnwrapError struct {
	mockPresentableSourceError
	err error
}

func (e mockUnwrapError) Unwrap() error { return e.err }

// mockUnwrapMultiError simulates a GN error that implements Unwrap() []error.
type mockUnwrapMultiError struct {
	mockPresentableSourceError
	errs []error
}

func (e mockUnwrapMultiError) Unwrap() []error { return e.errs }

// mockStackTraceError implements StackTraceError.
type mockStackTraceError struct {
	mockPresentableSourceError
	stack []error
}

func (e mockStackTraceError) Stack() []error {
	return e.stack
}

// makeLoc creates a basic syntax.Location at line 1, column 1.
func makeLoc(file string) syntax.Location {
	tokens, _ := syntax.Tokenize(syntax.LiteralInput{CustomName: file, Bytes: []byte("x")})
	return tokens[0].Range().Begin()
}

func TestFormatError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "common",
			err: mockPresentableSourceError{
				msg: "Something broke.",
				loc: makeLoc("//BUILD.gn"),
			},
			want: `ERROR at //BUILD.gn:1:1: Something broke.
`,
		},
		{
			name: "nolocation",
			err:  mockPresentableError{msg: "Something broke but I don't know where."},
			want: `ERROR Something broke but I don't know where.
`,
		},
		{
			name: "withhelp",
			err: mockPresentableSourceError{
				msg:  "A bad thing happened.",
				help: "Do this instead.",
				loc:  makeLoc("//BUILD.gn"),
			},
			want: `ERROR at //BUILD.gn:1:1: A bad thing happened.
Do this instead.
`,
		},
		{
			name: "nil",
			err:  nil,
			want: "",
		},
		{
			name: "native",
			err:  errors.New("golang error"),
			want: `ERROR golang error
`,
		},
		{
			name: "unwrapsingle",
			err: mockUnwrapError{
				mockPresentableSourceError: mockPresentableSourceError{
					msg: "Something failed.",
					loc: makeLoc("//BUILD.gn"),
				},
				err: mockPresentableSourceError{
					msg: "Where it came from.",
					loc: makeLoc("//BUILD.gn"),
				},
			},
			want: `ERROR at //BUILD.gn:1:1: Something failed.
See //BUILD.gn:1:1: Where it came from.
`,
		},
		{
			name: "unwrapmulti",
			err: mockUnwrapMultiError{
				mockPresentableSourceError: mockPresentableSourceError{
					msg: "Multiple things failed.",
					loc: makeLoc("//BUILD.gn"),
				},
				errs: []error{
					mockPresentableSourceError{
						msg: "First failure.",
						loc: makeLoc("//BUILD.gn"),
					},
					mockPresentableSourceError{
						msg: "Second failure.",
						loc: makeLoc("//BUILD.gn"),
					},
				},
			},
			want: `ERROR at //BUILD.gn:1:1: Multiple things failed.
See //BUILD.gn:1:1: First failure.
See //BUILD.gn:1:1: Second failure.
`,
		},
		{
			name: "stacktrace",
			err: mockStackTraceError{
				mockPresentableSourceError: mockPresentableSourceError{
					msg: "whence it was imported.",
					loc: makeLoc("//BUILD.gn"),
				},
				stack: []error{
					mockPresentableSourceError{
						msg: "//foo.gni is part of an import loop.",
						loc: makeLoc("//baz.gni"),
					},
					mockPresentableSourceError{
						msg: "whence it was imported.",
						loc: makeLoc("//bar.gni"),
					},
					mockPresentableSourceError{
						msg: "whence it was imported.",
						loc: makeLoc("//foo.gni"),
					},
				},
			},
			want: `ERROR at //baz.gni:1:1: //foo.gni is part of an import loop.
See //bar.gni:1:1: whence it was imported.
See //foo.gni:1:1: whence it was imported.
See //BUILD.gn:1:1: whence it was imported.
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := StripANSIEscapeCodes(FormatError(tc.err))
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("FormatError() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
