// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package fs

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unique"
)

// SourceFile represents a file within the source tree. Always begins in a slash, never
// ends in one.
type SourceFile struct {
	value unique.Handle[string]
}

// MakeSourceFile creates a source file representation from a known absolute source file.
// Always begins in a slash.
func MakeSourceFile(value string) (SourceFile, error) {
	if !strings.HasPrefix(value, "/") {
		return SourceFile{}, fmt.Errorf("should start with slash")
	}
	if endsWithSlash(value) {
		return SourceFile{}, fmt.Errorf("should not end with slash")
	}
	return SourceFile{
		value: unique.Make(NormalizePath(value)),
	}, nil
}

// Equal returns whether the source files are equal i.e. refer to the same file.
func (s SourceFile) Equal(other SourceFile) bool {
	if s == (SourceFile{}) || other == (SourceFile{}) {
		return s == other
	}
	return s.Filename() == other.Filename()
}

// Filename returns the source file name.
func (s SourceFile) Filename() string {
	return s.value.Value()
}

// Base returns the last element of the path.
func (s SourceFile) Base() string {
	return path.Base(s.value.Value())
}

// Dir returns the directory containing this file.
func (s SourceFile) Dir() SourceDir {
	// SourceFile guarantees value starts with / and does not end with /.
	// Thus there is always at least one slash.
	f := s.Filename()
	lastSlash := strings.LastIndex(f, "/")
	return makeSourceDirInternal(f[:lastSlash+1])
}

// Resolve resolves this source file relative to some given source root.
// (This does not have to be the source root of the build tree.)
func (s SourceFile) Resolve(sourceRoot string) string {
	if !IsPathSourceAbsolute(s.Filename()) {
		// TODO: handle windows properly like ResolvePath in filesystem_utils.cc does
		return s.Filename()
	}
	return filepath.ToSlash(path.Join(sourceRoot, strings.TrimPrefix(s.Filename(), "//")))
}
