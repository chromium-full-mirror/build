// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"go.chromium.org/build/gong/gn/build/graph"
	"go.chromium.org/build/gong/gn/build/graph/schemas"
	"go.chromium.org/build/gong/gn/resolve"
)

// FunctionMap maps GN buildfile functions to their implementations.
var FunctionMap = map[string]resolve.FunctionInfo{
	"action":                targetFunction{schema: &schemas.ActionSchema},
	"assert":                resolve.AssertFunction{},
	"config":                configFunction{},
	"copy":                  targetFunction{schema: &schemas.CopySchema},
	"executable":            targetFunction{schema: &schemas.ExecutableSchema},
	"filter_exclude":        filterExcludeFunction{},
	"filter_include":        filterIncludeFunction{},
	"getenv":                getenvFunction{},
	"group":                 targetFunction{schema: &schemas.GroupSchema},
	"import":                importFunction{},
	"len":                   lenFunction{},
	"path_exists":           pathExistsFunction{},
	"print":                 printFunction{},
	"rebase_path":           rebasePathFunction{},
	"rust_library":          targetFunction{schema: &schemas.RustLibrarySchema},
	"set_default_toolchain": setDefaultToolchainFunction{},
	"set_defaults":          setDefaultsFunction{},
	"shared_library":        targetFunction{schema: &schemas.SharedLibrarySchema},
	"static_library":        targetFunction{schema: &schemas.StaticLibrarySchema},
	"string_hash":           stringHashFunction{},
	"string_join":           stringJoinFunction{},
	"string_replace":        stringReplaceFunction{},
	"string_split":          stringSplitFunction{},
	"template":              templateFunction{},
	"toolchain":             toolchainFunction{},
	"tool":                  graph.ToolFunction{},
}
