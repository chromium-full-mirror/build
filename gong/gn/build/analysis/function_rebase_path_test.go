// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"errors"
	"testing"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

func TestRebasePathFunction(t *testing.T) {
	f := rebasePathFunction{}
	for _, tc := range []struct {
		name    string
		args    []resolve.Value
		curDir  fs.SourceDir
		want    string
		wantErr any
	}{
		{
			name: "source-absolute paths",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo/bar.txt"),
				resolve.NewOriginlessStringValue("//foo/"),
			},
			want: "bar.txt",
		},
		{
			name: "source-absolute up one level",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo/bar.txt"),
				resolve.NewOriginlessStringValue("//baz/"),
			},
			want: "../foo/bar.txt",
		},
		{
			name: "directory path",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo/bar/"),
				resolve.NewOriginlessStringValue("//baz/"),
			},
			want: "../foo/bar/",
		},
		{
			name: "new_base is a file",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo/bar.txt"),
				resolve.NewOriginlessStringValue("//baz/qux.txt"),
			},
			// This seems incorrect but matches C++ GN behavior at time of writing.
			// To reproduce, in function_rebase_path_unittest.cc, add the following:
			//     EXPECT_EQ("../../foo/bar.txt", RebaseOne(scope, "//foo/bar.txt", "//baz/qux.txt", "."));
			// Adding such a test should pass.
			want: "../../foo/bar.txt",
		},
		{
			name:    "too few args",
			args:    []resolve.Value{},
			wantErr: &resolve.ArgumentCountError{},
		},
		{
			name:    "too many args",
			args:    []resolve.Value{&resolve.StringValue{}, &resolve.StringValue{}, &resolve.StringValue{}, &resolve.StringValue{}},
			wantErr: &resolve.ArgumentCountError{},
		},
		{
			name: "invalid rebase input type",
			args: []resolve.Value{
				&resolve.IntegerValue{},
				resolve.NewOriginlessStringValue("//foo/"),
			},
			wantErr: &resolve.TypeError{},
		},
		{
			name: "invalid rebase new_base type",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo"),
				&resolve.IntegerValue{},
			},
			wantErr: &resolve.TypeError{},
		},
		{
			name: "list input",
			args: []resolve.Value{
				resolve.NewOriginlessListValue([]resolve.Value{
					resolve.NewOriginlessStringValue("foo.txt"),
					resolve.NewOriginlessStringValue("bar.txt"),
				}),
				resolve.NewOriginlessStringValue("//out/Debug/"),
				resolve.NewOriginlessStringValue("."),
			},
			curDir: mustDir(t, "//gn/"),
			want:   `["../../gn/foo.txt", "../../gn/bar.txt"]`,
		},
		{
			name: "relative input",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("foo/bar.txt"),
				resolve.NewOriginlessStringValue("//foo/"),
			},
			curDir: mustDir(t, "//"),
			want:   "bar.txt",
		},
		{
			name: "relative input with non-root curDir",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("bar.txt"),
				resolve.NewOriginlessStringValue("//"),
			},
			curDir: mustDir(t, "//foo/"),
			want:   "foo/bar.txt",
		},
		{
			name: "empty new_base (explicit)",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo/bar.txt"),
				resolve.NewOriginlessStringValue(""),
			},
			want: "foo/bar.txt",
		},
		{
			name: "empty new_base (default)",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo/bar.txt"),
			},
			want: "foo/bar.txt",
		},
		{
			name: "empty new_base with directory input",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo/bar/"),
				resolve.NewOriginlessStringValue(""),
			},
			want: "foo/bar",
		},
		{
			name: "empty new_base with list input",
			args: []resolve.Value{
				resolve.NewOriginlessListValue([]resolve.Value{
					resolve.NewOriginlessStringValue("//foo/bar.txt"),
					resolve.NewOriginlessStringValue("//baz/qux/"),
				}),
				resolve.NewOriginlessStringValue(""),
			},
			want: `["foo/bar.txt", "baz/qux"]`,
		},
		{
			name: "relative new_base",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo/bar.txt"),
				resolve.NewOriginlessStringValue("baz/"),
			},
			curDir: mustDir(t, "//"),
			want:   "../foo/bar.txt",
		},
		{
			name: "relative new_base with non-root curDir",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo/bar.txt"),
				resolve.NewOriginlessStringValue("."),
			},
			curDir: mustDir(t, "//baz/"),
			want:   "../foo/bar.txt",
		},
		{
			name: "relative input and new_base with non-root curDir",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("sub/file.txt"),
				resolve.NewOriginlessStringValue("."),
			},
			curDir: mustDir(t, "//foo/bar/"),
			want:   "sub/file.txt",
		},
		{
			name: "directory path with non-root curDir",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("sub/dir/"),
				resolve.NewOriginlessStringValue("//"),
			},
			curDir: mustDir(t, "//foo/"),
			want:   "foo/sub/dir/",
		},
		{
			name: "relative input dot",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("."),
				resolve.NewOriginlessStringValue("//"),
			},
			curDir: mustDir(t, "//foo/"),
			want:   "foo/",
		},
		{
			name: "relative input double dot",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue(".."),
				resolve.NewOriginlessStringValue("//"),
			},
			curDir: mustDir(t, "//foo/bar/"),
			want:   "foo/",
		},
		{
			name: "relative input path with dot",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("sub/."),
				resolve.NewOriginlessStringValue("//"),
			},
			curDir: mustDir(t, "//foo/"),
			want:   "foo/sub/",
		},
		{
			name: "relative input empty string",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue(""),
				resolve.NewOriginlessStringValue("//"),
			},
			curDir:  mustDir(t, "//foo/"),
			wantErr: "empty directory path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := resolve.NewScope(
				&scopeContext{
					settings:  NewSettings(&environment.BuildSettings{}),
					sourceDir: tc.curDir,
				},
				nil,
				nil,
			)
			callNode := &parse.FunctionCallNode{
				Function: syntax.MakeToken(syntax.TokenIdentifier, "rebase_path"),
			}

			got, err := f.Run(scope, callNode, tc.args)

			wantErr := tc.wantErr != nil
			gotErr := err != nil

			if gotErr != wantErr {
				t.Fatalf("Run(...) got err=%v (%T), wantErr %T", err, err, tc.wantErr)
			}

			if tc.wantErr != nil {
				// HACK: temporary until fs.SourceDir's resolve relative functions return concrete error types.
				// This needs a larger refactor so leave for followup.
				if wantErrStr, ok := tc.wantErr.(string); ok {
					if err.Error() != wantErrStr {
						t.Errorf("Run(...) got err=%v, want string %q", err, wantErrStr)
					}
					return
				}
				if !errors.As(err, tc.wantErr) {
					t.Errorf("Run(...) got err=%v (%T), wantErr %T", err, err, tc.wantErr)
				}
				return
			}

			if tc.want != got.RawGNString() {
				t.Errorf("Run(...) got %q; want %q", got.RawGNString(), tc.want)
			}
		})
	}
}
