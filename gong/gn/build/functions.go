// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"go.chromium.org/build/gong/gn/resolve"
)

// FunctionMap returns a map of GN buildfile functions to their implementations.
// It requires the top-level BuildSettings object.
func FunctionMap(buildSettings *BuildSettings) map[string]resolve.FunctionInfo {
	return map[string]resolve.FunctionInfo{
		"action":                actionFunction{},
		"assert":                resolve.AssertFunction{},
		"rebase_path":           &rebasePathFunction{buildSettings: buildSettings},
		"set_defaults":          &setDefaultsFunction{},
		"set_default_toolchain": setDefaultToolchainFunction{},
	}
}
