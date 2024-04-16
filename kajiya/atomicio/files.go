// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package atomicio provides atomic I/O operations that are used in various places in Kajiya.
package atomicio

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile writes data to a file named by filename by first writing to a
// temporary file in the same directory, then renaming it to the final name.
func WriteFile(filename string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(filename), "tmp_")
	if err != nil {
		return err
	}

	// Best effort cleanup in case something goes wrong.
	defer func() {
		if f != nil {
			if err := f.Close(); err != nil {
				fmt.Printf("Failed to close temporary file %q: %v", f.Name(), err)
			}
			if err := os.Remove(f.Name()); err != nil {
				fmt.Printf("Failed to remove temporary file %q: %v", f.Name(), err)
			}
		}
	}()

	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filename); err != nil {
		return err
	}
	f = nil // prevent defer from trying to remove the temporary file

	return nil
}
