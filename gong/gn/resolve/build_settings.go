// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import "path/filepath"

// BuildSettings represents settings for one build, which is one toplevel output directory.
// There may be multiple Settings objects that refer to this, one for each toolchain.
type BuildSettings struct {
	// DotfileName refers to the dotfile for this build.
	DotfileName string
	// rootPath is absolute path of the source root on the local system. Everything is
	// relative to this. Does not end in a [back]slash.
	//
	// WARNING: Unlike C++ GN we assume UTF-8 can safely handle file paths.
	// Hence, we use string directly with filepath rather than a "FilePath" struct.
	// This may lead to unexpected behavioral differences on Windows,
	// because C++ GN attempts to use UTF-16 (char16_t) on Windows.
	//
	// TODO: Investigate if this causes problems?
	rootPath string
}

// SetRootPath sets the absolute path of the source root on the local system.
func (bs *BuildSettings) SetRootPath(path string) {
	bs.rootPath = filepath.ToSlash(path)
}
