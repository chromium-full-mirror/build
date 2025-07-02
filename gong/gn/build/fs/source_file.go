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

func makeSourceFile(value string) (SourceFile, error) {
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

// Filename returns the source file name.
func (s SourceFile) Filename() string {
	return s.value.Value()
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
