// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package fs

import (
	"fmt"
	"strings"
	"unique"
)

// SourceDir represents a directory within the source tree. Source dirs begin and end in
// slashes.
//
// If there is one slash at the beginning, it will mean a system-absolute file
// path. On Windows, absolute system paths will be of the form "/C:/foo/bar".
//
// Two slashes at the beginning indicate a path relative to the source root.
type SourceDir struct {
	value unique.Handle[string]
}

// MakeSourceDir creates a source dir representation from a path string.
// The provided path string must start with a slash. If the path does not end
// with a slash, it will be added.
func MakeSourceDir(value string) (SourceDir, error) {
	if !strings.HasPrefix(value, "/") {
		return SourceDir{}, fmt.Errorf("path must start with a slash: %q", value)
	}
	return makeSourceDirInternal(value), nil
}

// makeSourceDirInternal creates a source dir representation from a path string.
// It does not validate the path starts with a slash.
func makeSourceDirInternal(value string) SourceDir {
	if !endsWithSlash(value) {
		return SourceDir{
			value: unique.Make(value + "/"),
		}
	}
	return SourceDir{
		value: unique.Make(value),
	}
}

// Path returns the directory path string, always ending in /.
//
// This is equivalent to C++ GN's SourceDir::value().
func (d SourceDir) Path() string {
	return d.value.Value()
}

// ResolveRelativeFile resolves a user-supplied path relative to this directory.
//
// The input path can be:
//   - Source-absolute ("//foo/bar.txt"): Returns the normalized source-absolute path.
//   - System-absolute ("/foo/bar.txt"): Returns the normalized system-absolute path.
//   - Relative ("baz.txt", "../baz.txt"): Resolves relative to this directory.
func (d SourceDir) ResolveRelativeFile(path string) (SourceFile, error) {
	// TODO: This is a placeholder implementation until ResolveRelativeAs is implemented,
	// because both ResolveRelativeDir and this function should then rely on that common
	// function.
	if path == "" {
		return SourceFile{}, fmt.Errorf("empty file path")
	}
	norm := NormalizePath(path)

	// Handle source-absolute paths and system-absolute paths.
	if IsPathSourceAbsolute(norm) || strings.HasPrefix(norm, "/") {
		return MakeSourceFile(norm)
	}

	// Handle relative paths.
	// Simple string concatenation is safe here because SourceDir always ends in /
	// and we know path does not start with /, and NormalizePath will handle ".." resolution.
	return MakeSourceFile(d.value.Value() + norm)
}

// ResolveRelativeDir resolves a user-supplied path relative to this directory,
// returning a SourceDir.
func (d SourceDir) ResolveRelativeDir(path string) (SourceDir, error) {
	// TODO: This is a placeholder implementation until ResolveRelativeAs is implemented,
	// because both ResolveRelativeFile and this function should then rely on that common
	// function.
	if path == "" {
		return SourceDir{}, fmt.Errorf("empty directory path")
	}

	// Handle source-absolute paths and system-absolute paths.
	if IsPathSourceAbsolute(path) || strings.HasPrefix(path, "/") {
		return MakeSourceDir(NormalizePath(path))
	}

	// Handle relative paths.
	// Simple string concatenation is safe here because SourceDir always ends in /
	// and we know path does not start with /, and NormalizePath will handle ".." resolution.
	return MakeSourceDir(NormalizePath(d.value.Value() + path))
}
