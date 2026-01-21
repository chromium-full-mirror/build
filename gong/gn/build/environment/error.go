// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package environment

import (
	"go.chromium.org/build/gong/gn/syntax"
)

// BuildError creates a GN error without a location.
func BuildError(message, helpText string) error {
	return syntax.MakeErrorAt(syntax.Location{}, nil, syntax.ErrUnknown, message, helpText)
}
