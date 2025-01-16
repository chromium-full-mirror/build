// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package gn

import (
	"fmt"
	"os"
)

// InputFile represents a lazy-loadable file.
type InputFile struct {
	// The virtual name for representing this file. This does not take into
	// account whether the file was loaded from the secondary source tree (see
	// BuildSettings secondarySourcePath).
	name SourceFile

	contentsLoaded bool
	contents       string
}

func newInputFile(name, path string) (*InputFile, error) {
	source, err := makeSourceFile(name)
	if err != nil {
		return nil, err
	}
	inputFile := &InputFile{name: source}
	err = inputFile.Load(path)
	if err != nil {
		return nil, err
	}
	return inputFile, nil
}

// Load loads the given file synchronously.
func (f *InputFile) Load(systemPath string) error {
	b, err := os.ReadFile(systemPath)
	if err != nil {
		return fmt.Errorf("failed to load path: %w", err)
	}
	f.contents = string(b)
	f.contentsLoaded = true
	return nil
}
