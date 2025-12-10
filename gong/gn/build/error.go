// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"go.chromium.org/build/gong/gn/syntax"
)

// makeError creates a GN error without a location.
func makeError(message, helpText string) error {
	return syntax.MakeErrorAt(syntax.Location{}, nil, syntax.ErrUnknown, message, helpText)
}
