// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package environment represents the runtime state of a build.
package environment

import (
	"path/filepath"

	"go.chromium.org/build/gong/gn/build/fs"
)

// BuildSettings represents settings for one build, which is one toplevel output directory.
// There may be multiple Settings objects that refer to this, one for each toolchain.
// TODO: rename this to just Settings (build.BuildSettings not great name), rename Settings to something else?
type BuildSettings struct {
	// DotfileName refers to the dotfile for this build.
	DotfileName string
	// RootPath is absolute path of the source root on the local system. Everything is
	// relative to this. Does not end in a [back]slash.
	//
	// WARNING: Unlike C++ GN we assume UTF-8 can safely handle file paths.
	// Hence, we use string directly with filepath rather than a "FilePath" struct.
	// This may lead to unexpected behavioral differences on Windows,
	// because C++ GN attempts to use UTF-16 (char16_t) on Windows.
	//
	// TODO: Investigate if this causes problems?
	//
	// TODO: Consider whether it would improve ergonomics to use io/fs?
	RootPath string
	// When nonempty, specifies a parallel directory higherarchy in which to
	// search for buildfiles if they're not found in the root higherarchy. This
	// allows us to keep buildfiles in a separate tree during development.
	secondarySourcePath string
	// Root target label.
	RootTargetLabel Label
	// Path of the python executable to run scripts with.
	PythonPath string
	// BuildDir is the root of all output files. The default toolchain
	// files go into here, and non-default toolchains will have separate
	// toolchain-specific root directories inside this.
	//
	// TODO: Consider whether it would improve ergonomics to use io/fs?
	BuildDir fs.SourceDir
	// BuildConfigFile is a reference to the build config file for this build.
	// It is expected that the root .gn file defines a `buildconfig` variable
	// that points to the location of the build config file.
	BuildConfigFile fs.SourceFile
	// PrintCallback overrides the behavior of the print() function.
	// If nil, output will be printed to the console.
	PrintCallback func(string)
}

// FullPath returns the full absolute OS path corresponding to the given
// file in the root source tree.
//
// This is equivalent to C++ GN's BuildSettings::GetFullPath(const SourceFile& file).
func (bs *BuildSettings) FullPath(file fs.SourceFile) string {
	return filepath.ToSlash(file.Resolve(bs.RootPath))
}

// FullDirPath returns the full absolute OS path corresponding to the given
// directory in the root source tree.
//
// This is equivalent to C++ GN's BuildSettings::GetFullPath(const SourceDir& dir).
func (bs *BuildSettings) FullDirPath(dir fs.SourceDir) string {
	return filepath.ToSlash(dir.Resolve(bs.RootPath))
}

// FullPathSecondary returns the absolute OS path inside the secondary
// source path. Will return an empty string if the secondary source path
// is empty. When loading a buildfile, the GetFullPath should always be
// consulted first.
func (bs *BuildSettings) FullPathSecondary(file fs.SourceFile) string {
	return filepath.ToSlash(file.Resolve(bs.secondarySourcePath))
}

// HasSecondarySourcePath returns whether an alternate directory tree to
// find input files is available. This behavior is intended to be used when
// BUILD.gn files can't be checked in to certain source directories for
// whatever reason.
func (bs *BuildSettings) HasSecondarySourcePath() bool {
	return bs.secondarySourcePath != ""
}
