// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/graph"
	"go.chromium.org/build/gong/gn/build/graph/schemas"
	"go.chromium.org/build/gong/gn/resolve"
)

// FunctionMap returns a map of GN buildfile functions to their implementations.
// It requires the top-level BuildSettings object.
func FunctionMap(buildSettings *environment.BuildSettings) map[string]resolve.FunctionInfo {
	return map[string]resolve.FunctionInfo{
		"action":                targetFunction{schema: &schemas.ActionSchema},
		"assert":                resolve.AssertFunction{},
		"config":                configFunction{},
		"copy":                  targetFunction{schema: &schemas.CopySchema},
		"executable":            targetFunction{schema: &schemas.ExecutableSchema},
		"rebase_path":           &rebasePathFunction{buildSettings: buildSettings},
		"rust_library":          targetFunction{schema: &schemas.RustLibrarySchema},
		"set_defaults":          &setDefaultsFunction{},
		"set_default_toolchain": setDefaultToolchainFunction{},
		"shared_library":        targetFunction{schema: &schemas.SharedLibrarySchema},
		"static_library":        targetFunction{schema: &schemas.StaticLibrarySchema},
		"toolchain":             toolchainFunction{},
		"tool":                  graph.ToolFunction{},
	}
}
