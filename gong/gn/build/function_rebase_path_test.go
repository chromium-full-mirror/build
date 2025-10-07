// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"testing"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

func TestRebasePathFunction(t *testing.T) {
	f := &rebasePathFunction{buildSettings: &BuildSettings{}}
	for _, tc := range []struct {
		name        string
		args        []resolve.Value
		want        string
		wantErrKind syntax.ErrKind
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
			want: "../foo/bar",
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
			name:        "too few args",
			args:        []resolve.Value{},
			wantErrKind: syntax.ErrArgumentCount,
		},
		{
			name:        "too many args",
			args:        []resolve.Value{&resolve.StringValue{}, &resolve.StringValue{}, &resolve.StringValue{}, &resolve.StringValue{}},
			wantErrKind: syntax.ErrArgumentCount,
		},
		{
			name:        "invalid input type",
			args:        []resolve.Value{&resolve.IntegerValue{}},
			wantErrKind: syntax.ErrTypeMismatch,
		},
		{
			name: "invalid new_base type",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo"),
				&resolve.IntegerValue{},
			},
			wantErrKind: syntax.ErrTypeMismatch,
		},
		{
			name:        "list input not implemented",
			args:        []resolve.Value{&resolve.ListValue{}},
			wantErrKind: syntax.ErrNotImplemented,
		},
		{
			name: "relative input not implemented",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("foo/bar.txt"),
				resolve.NewOriginlessStringValue("//foo/"),
			},
			wantErrKind: syntax.ErrNotImplemented,
		},
		{
			name: "empty new_base not implemented",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo/bar.txt"),
				resolve.NewOriginlessStringValue(""),
			},
			wantErrKind: syntax.ErrNotImplemented,
		},
		{
			name: "relative new_base not implemented",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo/bar.txt"),
				resolve.NewOriginlessStringValue("foo/"),
			},
			wantErrKind: syntax.ErrNotImplemented,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := &resolve.Scope{}
			callNode := &parse.FunctionCallNode{
				Function: syntax.MakeToken(syntax.TokenIdentifier, "rebase_path"),
			}

			got, err := f.Run(scope, callNode, tc.args)

			wantErr := tc.wantErrKind != ""
			gotErr := err != nil

			if gotErr != wantErr {
				t.Fatalf("Run(...) got err=%v, wantErr=%v (kind %s)", err, wantErr, tc.wantErrKind)
			}

			if gotErr {
				if match, gotErrKind := syntax.AsErrKind(err, tc.wantErrKind); match == nil {
					t.Fatalf("Run(...) got err=%v (kind %s), wantErrKind=%s", err, gotErrKind, tc.wantErrKind)
				}
				return
			}

			if tc.want != got.RawGNString() {
				t.Errorf("Run(...) got %q; want %q", got.RawGNString(), tc.want)
			}
		})
	}
}
