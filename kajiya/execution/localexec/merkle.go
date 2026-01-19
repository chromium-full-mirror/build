// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package localexec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/model"
)

// MaterializeDirectory materializes the *repb.Directory d into the empty directory
// at the given path.
func MaterializeDirectory(cas *blobstore.ContentAddressableStorage, path string, d *model.KajiyaDirectory, withOutputs bool) error {
	// Materialize all regular input files into the directory.
	for _, f := range d.Files {
		filePath := filepath.Join(path, f.Name)
		if err := MaterializeFile(cas, filePath, f); err != nil {
			return err
		}
	}

	// Create all symlinks.
	for _, sl := range d.Symlinks {
		slPath := filepath.Join(path, sl.Name)
		if err := os.Symlink(sl.Target, slPath); err != nil {
			return fmt.Errorf("failed to create input symlink: %w", err)
		}
	}

	// Create all subdirectories.
	for _, sd := range d.Dirs {
		if err := os.Mkdir(filepath.Join(path, sd), 0755); err != nil {
			return fmt.Errorf("failed to create input directory: %w", err)
		}
	}

	// Create all outputs, if requested.
	if withOutputs {
		if err := CreateOutputDirectories(path, d); err != nil {
			return err
		}
	}

	// Finally, set the directory properties. We have to do this after the files have been
	// materialized, because otherwise the mtime of the directory would be updated to the
	// current time.
	if d.UnixMode != 0755 {
		if err := os.Chmod(path, d.UnixMode); err != nil {
			return fmt.Errorf("failed to set mode: %w", err)
		}
	}
	if !d.Mtime.IsZero() {
		if err := os.Chtimes(path, d.Mtime, d.Mtime); err != nil {
			return fmt.Errorf("failed to set mtime: %w", err)
		}
	}

	return nil
}

// CreateOutputDirectories creates the parent directories of all outputs under directory `d`.
func CreateOutputDirectories(path string, d *model.KajiyaDirectory) error {
	seen := make(map[string]bool)
	for _, output := range d.Outputs {
		outputDir := filepath.Dir(output.Name)
		if _, ok := seen[outputDir]; ok {
			continue
		}
		// Create all parent directories.
		parts := strings.Split(outputDir, "/")
		dirName := ""
		for i := range parts {
			dirName = filepath.Join(dirName, parts[i])
			if _, ok := seen[dirName]; ok {
				continue
			}
			if err := os.Mkdir(filepath.Join(path, dirName), 0755); err != nil {
				return fmt.Errorf("failed to create output directory: %w", err)
			}
			seen[dirName] = true
		}
	}
	return nil
}

// MaterializeFile downloads the given file from the CAS and writes it to the given path.
func MaterializeFile(cas *blobstore.ContentAddressableStorage, filePath string, f model.KajiyaFile) error {
	// Calculate the file permissions from all relevant fields.
	if err := cas.LinkTo(f.Digest, filePath); err != nil {
		return fmt.Errorf("failed to link to file in CAS: %w", err)
	}

	if f.UnixMode != 0644 {
		if err := os.Chmod(filePath, f.UnixMode); err != nil {
			return fmt.Errorf("failed to set mode: %w", err)
		}
	}

	if !f.Mtime.IsZero() {
		if err := os.Chtimes(filePath, f.Mtime, f.Mtime); err != nil {
			return fmt.Errorf("failed to set mtime: %w", err)
		}
	}

	return nil
}
