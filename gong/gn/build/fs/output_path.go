// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package fs

// OutputPath represents a path relative to the output directory. It can represent a file or a directory.
type OutputPath struct {
	buildDir SourceDir // The root build directory
	value    string    // Path relative to build directory
}

// MakeOutputPath creates an OutputPath given its build directory and path relative to the build directory.
func MakeOutputPath(buildDir SourceDir, value string) OutputPath {
	return OutputPath{buildDir: buildDir, value: value}
}

// Equal returns whether the output paths are equal i.e. refer to the same path in the same build dir.
func (s OutputPath) Equal(other OutputPath) bool {
	if s == (OutputPath{}) || other == (OutputPath{}) {
		return s == other
	}
	return s.value == other.value && s.buildDir.Equal(other.buildDir)
}

// BuildDir returns the root build directory.
func (p OutputPath) BuildDir() SourceDir {
	return p.buildDir
}

// Path returns the path string relative to the build directory.
func (p OutputPath) Path() string {
	return p.value
}

// AsSourceFile returns the underlying SourceFile relative to the build directory.
func (p OutputPath) AsSourceFile() (SourceFile, error) {
	return p.buildDir.ResolveRelativeFile(p.value)
}

// AsSourceDir returns the underlying SourceDir relative to the build directory.
func (p OutputPath) AsSourceDir() (SourceDir, error) {
	return p.buildDir.ResolveRelativeDir(p.value)
}
