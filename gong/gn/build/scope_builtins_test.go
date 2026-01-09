// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"testing"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/resolve"
)

func TestBuiltinProvider(t *testing.T) {
	buildDir, err := fs.MakeSourceDir("//out/Debug/")
	if err != nil {
		t.Fatalf("setup error: failed to make build dir: %v", err)
	}
	scope := resolve.NewScope(
		&scopeContext{},
		&builtinProvider{
			buildSettings: &BuildSettings{
				BuildDir:   buildDir,
				pythonPath: "python3",
			},
		},
		map[string]resolve.FunctionInfo{},
	)

	for _, tc := range []struct {
		name          string
		buildSettings *BuildSettings
		ident         string
		wantValue     string
		wantOk        bool
	}{
		{
			name:      "python_path",
			ident:     "python_path",
			wantValue: "python3",
		},
		{
			name:      "root_build_dir",
			ident:     "root_build_dir",
			wantValue: "//out/Debug",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotValue := scope.Value(tc.ident, false)

			if gotValue == nil {
				t.Errorf("ProgrammaticBuiltin(%q) value = nil; want %q", tc.ident, tc.wantValue)
				return
			}

			if gotStr := gotValue.RawGNString(); gotStr != tc.wantValue {
				t.Errorf("ProgrammaticBuiltin(%q) value = %q; want %q", tc.ident, gotStr, tc.wantValue)
			}
		})
	}
}
