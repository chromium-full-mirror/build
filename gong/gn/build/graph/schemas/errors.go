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
