// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

// Toolchain is an item in the GN dependency graph that represents information
// on a specific toolchain. This data is filled in when we encounter a toolchain
// definition.
//
// This class is an Item so it can participate in dependency management. In
// particular, when a target uses a toolchain, it should have a dependency on
// that toolchain's object so that we can be sure we loaded the toolchain
// before generating the build for that target.
//
//nolint:unused
type Toolchain struct {
	itemInfo
	// The Settings of an Item is always the context in which the Item was
	// defined. For a toolchain this is confusing because this is NOT the
	// settings object that applies to the things in the toolchain.
	//
	// To get the Settings object corresponding to objects loaded in the context
	// of this toolchain (probably what you want instead), see
	// Loader.GetToolchainSettings(). Many toolchain objects may be created in a
	// given build, but only a few might be used, and the Loader is in charge of
	// this process.
	//
	// We also track the set of build files that may affect this target, please
	// refer to scopeContext for how this is determined.
	settings *Settings
}

func (Toolchain) compatibleWith(item Item) bool {
	switch item.(type) {
	case *Toolchain:
		return true
	}
	return false
}
