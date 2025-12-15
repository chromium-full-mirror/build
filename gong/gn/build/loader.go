// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"fmt"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/syntax"
)

// Loader manages execution of the different build files. It receives
// requests when new references are found, and also manages loading the
// build config files.
type Loader struct {
	buildSettings *BuildSettings

	// buildFileExtension is the additional extension for build files in this build.
	// The resulting file name will be "BUILD.<extension>.gn".
	buildFileExtension string
}

// MakeLoader creates a loader.
func MakeLoader(buildSettings *BuildSettings) Loader {
	return Loader{
		buildSettings: buildSettings,
	}
}

func (l *Loader) buildFileForLabel(label Label) (fs.SourceFile, error) {
	return fs.MakeSourceFile(label.dir + "BUILD" + l.buildFileExtension + ".gn")
}

// Load schedules a file load, noting down where the load came from.
func (l *Loader) Load(file fs.SourceFile, origin syntax.LocationRange) error {
	return fmt.Errorf("load not implemented. requested to load: %q", file.Filename())
}
