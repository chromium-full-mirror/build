// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package fs

import (
	"fmt"
	"path/filepath"
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
	// Unfortunately unique.Handle doesn't support checking initialization yet
	// (see https://github.com/golang/go/issues/73266), so until it does use this
	// flag to ensure methods don't cause nil panic.
	hasValue bool
}

// Equal returns whether the source dirs are equal i.e. refer to the same dir.
func (s SourceDir) Equal(other SourceDir) bool {
	if !s.hasValue && !other.hasValue {
		return true
	}
	if s.hasValue && other.hasValue {
		return s.value.Value() == other.value.Value()
	}
	return false
}

// Compare returns the result of comparing the two SourceDir paths lexographically.
func (d SourceDir) Compare(other SourceDir) int {
	if d.hasValue && other.hasValue {
		return strings.Compare(d.value.Value(), other.value.Value())
	}
	if d.hasValue {
		return 1
	}
	return -1
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

// MakeSourceDirFromPath the "best" [SourceDir] representing the given path. If it's
// inside the given sourceRoot, a source-relative directory will be returned (e.g.
// "//foo/bar.cc". If it's outside of the source root or the source root is
// empty, a system-absolute directory will be returned.
//
// Note that symlinks are not handled.
// This is equivalent to C++ GN's SourceDirForPath(const base::FilePath& source_root, const base::FilePath& path).
func MakeSourceDirFromPath(sourceRoot, path string) (SourceDir, error) {
	// If no source root, then treat as system absolute.
	if sourceRoot == "" {
		return makeAbsoluteSourceDir(path), nil
	}

	// Determine if path is inside sourceRoot.
	// filepath.Rel returns an error if the paths can't be made relative (e.g.
	// different drives on Windows). If it succeeds, we still need to check if
	// it had to step outside the root to get there (starts with "..").
	cleanSourceRoot := cleanInputPath(sourceRoot)
	cleanPath := cleanInputPath(path)
	rel, err := filepath.Rel(cleanSourceRoot, cleanPath)
	if err != nil || !filepath.IsLocal(rel) {
		return makeAbsoluteSourceDir(cleanPath), nil
	}

	// Path is inside source root.
	// Ensure normalize slashes to GN-internal, always use "/".
	if rel == "." {
		// If same dir then just return "//".
		return makeSourceDirInternal("//"), nil
	}
	slashRel := filepath.ToSlash(rel)
	return makeSourceDirInternal("//" + slashRel), nil
}

// makeAbsoluteSourceDir converts an OS-level absolute path into a GN system-absolute SourceDir.
func makeAbsoluteSourceDir(path string) SourceDir {
	slashPath := filepath.ToSlash(path)

	// On Windows, an absolute path might look like "C:/foo".
	// GN requires all system-absolute paths to start with a slash, e.g., "/C:/foo".
	if !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}

	return makeSourceDirInternal(slashPath)
}

// makeSourceDirInternal creates a source dir representation from a path string.
// It does not validate the path starts with a slash.
func makeSourceDirInternal(value string) SourceDir {
	if !endsWithSlash(value) {
		return SourceDir{
			value:    unique.Make(value + "/"),
			hasValue: true,
		}
	}
	return SourceDir{
		value:    unique.Make(value),
		hasValue: true,
	}
}

// Path returns the directory path string, always ending in /.
//
// This is equivalent to C++ GN's SourceDir::value().
func (d SourceDir) Path() string {
	if !d.hasValue {
		return ""
	}
	return d.value.Value()
}

// IsSourceAbsolute returns true if this path starts with a "//" which indicates a path
// from the source root.
func (d SourceDir) IsSourceAbsolute() bool {
	if !d.hasValue {
		return false
	}
	return IsPathSourceAbsolute(d.value.Value())
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
		// TODO: switch to concrete error type
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
		// TODO: switch to concrete error type
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

// Empty returns true if there is no directory.
func (d SourceDir) Empty() bool {
	return !d.hasValue || d.value.Value() == ""
}

// WithNoTrailingSlash returns a path that does not end with a slash.
func (d SourceDir) WithNoTrailingSlash() string {
	if !d.hasValue {
		return ""
	}
	path := d.value.Value()
	// Be careful not to trim if the input is just "/" or "//".
	// Source dirs begin and end in slashes, so len <= 2 is always "/" or "//".
	if len(path) > 2 {
		return path[:len(path)-1]
	}
	return path
}

// Resolve resolves this source file relative to some given source root.
// (This does not have to be the source root of the build tree.)
func (s SourceDir) Resolve(sourceRoot string) string {
	return ResolvePath(s.Path(), sourceRoot)
}
