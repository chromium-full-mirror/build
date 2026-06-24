// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/resolve"
)

func TestPathExistsFunction(t *testing.T) {
	tempDir := t.TempDir()
	err := os.Mkdir(filepath.Join(tempDir, "some-dir"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(tempDir, "some-dir", "foo.txt"), []byte("foo"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	// Convert the FS path to GN format for use in absolute FS path tests below.
	tempGNDir, err := fs.MakeSourceDirFromPath("", tempDir)
	if err != nil {
		t.Fatalf("failed to make source dir from %q: %v", tempDir, err)
	}

	for _, tc := range []struct {
		path string
		want bool
	}{
		{path: "//", want: true},
		{path: "//some-dir", want: true},
		{path: "//some-dir/", want: true},
		{path: "../some-dir", want: true},
		{path: "//some-dir/foo.txt", want: true},
		{path: "foo.txt", want: true},
		// Absolute FS paths.
		{path: tempGNDir.Path(), want: true},
		{path: tempGNDir.WithNoTrailingSlash() + "/some-dir/foo.txt", want: true},
		// Non-existing paths.
		{path: "//bar", want: false},
		{path: "bar", want: false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			scope := resolve.NewScope(
				&scopeContext{
					settings: NewSettings(
						&environment.BuildSettings{
							RootPath: tempDir,
						},
						NewImportManager(&fs.InputFileManager{}),
					),
					sourceDir: mustDir(t, "//some-dir/"),
				},
				nil,
			)

			args := []resolve.Value{resolve.NewOriginlessStringValue(tc.path)}
			val, err := pathExistsFunction{}.Run(scope, nil, args)
			if err != nil {
				t.Fatalf("path_exists(%q)=_,%v; want nil err", tc.path, err)
			}

			boolVal, err := resolve.AsValue[*resolve.BooleanValue](val)
			if err != nil {
				t.Fatalf("expected boolean value: %v", err)
			}
			if boolVal.Value() != tc.want {
				t.Errorf("path_exists(%q)=%v; want %v", tc.path, boolVal.Value(), tc.want)
			}
		})
	}
}

func TestPathExistsFunction_Errors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []resolve.Value
		wantErr any
	}{
		{
			name:    "zero",
			args:    []resolve.Value{},
			wantErr: &resolve.ArgumentCountError{},
		},
		{
			name: "multiple",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("a"),
				resolve.NewOriginlessStringValue("b"),
			},
			wantErr: &resolve.ArgumentCountError{},
		},
		{
			name:    "integer",
			args:    []resolve.Value{resolve.NewOriginlessIntegerValue(123)},
			wantErr: &resolve.TypeError{},
		},
		{
			name: "list",
			args: []resolve.Value{
				resolve.NewOriginlessListValue([]resolve.Value{resolve.NewOriginlessStringValue("a")}),
			},
			wantErr: &resolve.TypeError{},
		},
		{
			name:    "empty",
			args:    []resolve.Value{resolve.NewOriginlessStringValue("")},
			wantErr: &resolve.ValueError{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := resolve.NewScope(
				&scopeContext{
					settings: NewSettings(
						&environment.BuildSettings{},
						NewImportManager(&fs.InputFileManager{}),
					),
				},
				nil,
			)

			_, err := pathExistsFunction{}.Run(scope, nil, tc.args)
			gotErr := err != nil
			wantErr := tc.wantErr != nil

			if gotErr != wantErr {
				t.Errorf("path_exists err=%v (%T); wantErr %T", err, err, tc.wantErr)
			}

			if gotErr && !errors.As(err, tc.wantErr) {
				t.Errorf("path_exists err=%v (%T); wantErr %T", err, err, tc.wantErr)
			}
		})
	}
}
