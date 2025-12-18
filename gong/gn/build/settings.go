// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"go.chromium.org/build/gong/gn/resolve"
)

// Settings holds the settings for one toolchain invocation. There will be one
// Settings object for each toolchain type, each referring to the same
// BuildSettings object for shared stuff.
//
// The Toolchain object holds the set of stuff that is set by the toolchain
// declaration, which obviously needs to be set later when we actually parse
// the file with the toolchain declaration in it.
// TODO: rename this to something else, since build.BuildSettings would be better named build.Settings?
type Settings struct {
	buildSettings *BuildSettings
	// baseConfig is populated by the Loader when initializing the toolchain.
	// We don't touch it afterwards, as it's used as the top-level exec context for scopes
	// when executing buildfiles.
	baseConfig *resolve.Scope
}

// NewSettings creates a new Settings.
func NewSettings(buildSettings *BuildSettings) *Settings {
	return &Settings{
		buildSettings: buildSettings,
		baseConfig: resolve.NewScope(&builtinProvider{
			buildSettings: buildSettings,
		}, FunctionMap(buildSettings)),
	}
}

// BaseConfig returns the base config scope for this toolchain invocation.
// It implements resolve.ExecContext.
func (s *Settings) BaseConfig() *resolve.Scope {
	return s.baseConfig
}
