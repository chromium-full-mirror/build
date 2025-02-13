// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

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
}

// NewSettings creates a new Settings.
func NewSettings(buildSettings *BuildSettings) *Settings {
	return &Settings{
		buildSettings: buildSettings,
	}
}
