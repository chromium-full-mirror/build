// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"go.chromium.org/build/gong/gn/build/environment"
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
	buildSettings *environment.BuildSettings
	// baseConfig is populated by the Loader when initializing the toolchain.
	// We don't touch it afterwards, as it's used as the top-level exec context for scopes
	// when executing buildfiles.
	baseConfig *resolve.Scope
}

// NewSettings creates a new Settings.
func NewSettings(buildSettings *environment.BuildSettings) *Settings {
	s := &Settings{
		buildSettings: buildSettings,
	}
	s.baseConfig = s.NewScope()
	return s
}

// NewScope creates a new top-level scope for this toolchain invocation.
func (s *Settings) NewScope() *resolve.Scope {
	return resolve.NewScope(
		&scopeContext{
			settings:       s,
			targetDefaults: make(map[string]*resolve.Scope),
		},
		&builtinProvider{buildSettings: s.buildSettings},
		FunctionMap(s.buildSettings),
	)
}

// scopeContext is the exec context for a scope.
// This makes it possible to hold scope-local data such as target defaults that cannot be
// represented as simple GN values inside the scope itself, or should not be exposed.
type scopeContext struct {
	parent *scopeContext
	// The toolchain invocation this scope belongs to.
	settings *Settings
	// Flag to indicate that we're currently processing the build configuration file.
	// TODO: is it safe to allow users to directly flip this bool?
	// or do we need protection like C++ GN e.g.
	// https://source.chromium.org/gn/gn/+/main:src/gn/scope.cc;l=507-508;drc=feafd1012a32c05ec6095f69ddc3850afb621f3a
	processingBuildConfig bool
	// Receiver for the default toolchain label.
	// This is set by the Loader when initializing the default toolchain.
	defaultToolchainReceiver func(toolchainLabel environment.Label)
	// The target defaults for this scope.
	// Target defaults are scope-local, not toolchain-global.
	targetDefaults map[string]*resolve.Scope
}

// BaseConfig returns the base config for the scope.
func (s *scopeContext) BaseConfig() *resolve.Scope {
	return s.settings.baseConfig
}

// NestedContext prepares a new nested context when the resolve package needs to
// create a new nested scope.
func (s *scopeContext) NestedContext() resolve.ExecContext {
	return &scopeContext{
		parent:         s,
		settings:       s.settings,
		targetDefaults: make(map[string]*resolve.Scope),
	}
}

// isProcessingBuildConfig indicates if we're currently processing the build
// configuration file. This is true when processing the config file for any
// toolchain.
//
// Note that querying the state of the flag recursively checks all containing
// scopes until it reaches the top or finds the flag set.
func (s *scopeContext) isProcessingBuildConfig() bool {
	if s.processingBuildConfig {
		return true
	}
	if s.parent != nil {
		return s.parent.isProcessingBuildConfig()
	}
	return false
}
