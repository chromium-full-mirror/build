// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"errors"
	"testing"
	"testing/fstest"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

func TestImportFunction(t *testing.T) {
	files := fstest.MapFS{
		"root.gni": &fstest.MapFile{
			Data: []byte(`result = "hello_root"`),
		},
		"subdir/relative.gni": &fstest.MapFile{
			Data: []byte(`result = "hello_relative"`),
		},
	}

	for _, tc := range []struct {
		name    string
		args    []resolve.Value
		curDir  fs.SourceDir
		want    string
		wantErr any
	}{
		{
			name:    "zero",
			args:    []resolve.Value{},
			wantErr: resolve.ArgumentCountError{},
		},
		{
			name: "toomany",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("foo.gni"),
				resolve.NewOriginlessStringValue("bar.gni"),
			},
			wantErr: resolve.ArgumentCountError{},
		},
		{
			name:    "nonstring",
			args:    []resolve.Value{&resolve.IntegerValue{}},
			wantErr: resolve.TypeError{},
		},
		{
			name:    "resolvesrelative",
			args:    []resolve.Value{resolve.NewOriginlessStringValue("relative.gni")},
			curDir:  mustDir(t, "//subdir"),
			want:    "hello_relative",
			wantErr: nil,
		},
		{
			name:    "absolute",
			args:    []resolve.Value{resolve.NewOriginlessStringValue("//root.gni")},
			curDir:  mustDir(t, "//subdir"),
			want:    "hello_root",
			wantErr: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := NewSettings(
				&environment.BuildSettings{},
				NewImportManager(&fs.InputFileManager{FS: files}),
			)

			dest := resolve.NewScope(
				&scopeContext{settings: settings, sourceDir: tc.curDir},
				nil,
			)
			_, err := importFunction{}.Run(dest, &parse.FunctionCallNode{}, tc.args)
			wantErr := tc.wantErr != nil
			gotErr := err != nil

			if gotErr != wantErr {
				t.Fatalf("import(%v) got err=%v, want %T", tc.args, err, tc.wantErr)
			}
			if gotErr && !errors.As(err, &tc.wantErr) {
				t.Errorf("import(%v) got err=%v (%T), want %T", tc.args, err, err, tc.wantErr)
			}

			if !wantErr {
				if v := dest.Value("result", false); v == nil {
					t.Errorf("import(%v) = nil; want %q", tc.args, tc.want)
				} else if sv, ok := v.(*resolve.StringValue); !ok {
					t.Errorf("import(%v) = %v; want string", tc.args, v)
				} else if sv.RawGNString() != tc.want {
					t.Errorf("import(%v) = %q; want %q", tc.args, sv.RawGNString(), tc.want)
				}
			}
		})
	}
}
