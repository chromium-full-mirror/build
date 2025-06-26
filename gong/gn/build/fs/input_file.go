// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package fs provides representation of GN input files.
package fs

import (
	"fmt"
	"os"

	"go.chromium.org/build/gong/gn/syntax"
)

// InputFile represents a lazy-loadable file.
type InputFile struct {
	name     SourceFile
	contents []byte
}

// NewInputFile creates an input file from provided path.
func NewInputFile(name, path string) (*InputFile, error) {
	source, err := makeSourceFile(name)
	if err != nil {
		return nil, err
	}
	inputFile := &InputFile{name: source}
	err = inputFile.load(path)
	if err != nil {
		return nil, err
	}
	return inputFile, nil
}

// Load loads the given file synchronously.
func (f *InputFile) load(systemPath string) error {
	b, err := os.ReadFile(systemPath)
	if err != nil {
		return fmt.Errorf("failed to load path: %w", err)
	}
	f.contents = b
	return nil
}

// DisplayName is the virtual name for representing this file. This does not take into
// account whether the file was loaded from the secondary source tree (see
// BuildSettings secondarySourcePath).
func (f InputFile) DisplayName() string {
	return f.name.Filename()
}

// Contents represents contents of the file.
func (f InputFile) Contents() []byte {
	return f.contents
}

// Equal returns whether the input source is equivalent to the other.
func (f InputFile) Equal(other syntax.InputSource) bool {
	if inputFile, ok := other.(*InputFile); ok {
		return f.name.value == inputFile.name.value
	}
	return false
}
