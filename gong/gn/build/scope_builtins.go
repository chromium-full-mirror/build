// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/resolve"
)

// builtinProvider provides GN builtin variables.
type builtinProvider struct {
	buildSettings *BuildSettings
}

// ProgrammaticBuiltin implements resolve.ProgrammaticProvider.
func (p *builtinProvider) ProgrammaticBuiltin(ident string) (resolve.Value, bool) {
	switch ident {
	case "python_path":
		return resolve.NewOriginlessStringValue(p.buildSettings.pythonPath), true
	case "root_build_dir":
		return resolve.NewOriginlessStringValue(fs.DirectoryWithNoLastSlash(p.buildSettings.BuildDir)), true
	}
	return nil, false
}
