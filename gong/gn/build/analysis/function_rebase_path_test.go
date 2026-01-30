// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"errors"
	"testing"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

func TestRebasePathFunction(t *testing.T) {
	f := &rebasePathFunction{buildSettings: &environment.BuildSettings{}}
	for _, tc := range []struct {
		name    string
		args    []resolve.Value
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
			name:    "invalid input type",
			args:    []resolve.Value{&resolve.IntegerValue{}},
			wantErr: &resolve.TypeError{},
		},
		{
			name: "invalid new_base type",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo"),
				&resolve.IntegerValue{},
			},
			wantErr: &resolve.TypeError{},
		},
		{
			name:    "list input not implemented",
			args:    []resolve.Value{&resolve.ListValue{}},
			wantErr: &NotImplementedError{},
		},
		{
			name: "relative input",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("foo/bar.txt"),
				resolve.NewOriginlessStringValue("//foo/"),
			},
			// For prototype purposes, we assume Scope always has // as its current dir.
			// Therefore we are resolving // + foo/bar.txt = //foo/bar.txt relative to //foo/.
			want: "bar.txt",
		},
		{
			name: "empty new_base not implemented",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo/bar.txt"),
				resolve.NewOriginlessStringValue(""),
			},
			wantErr: &NotImplementedError{},
		},
		{
			name: "relative new_base",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("//foo/bar.txt"),
				resolve.NewOriginlessStringValue("baz/"),
			},
			// For prototype purposes, we assume Scope always has // as its current dir.
			// Therefore we are resolving relative to // + baz/ = //baz/.
			want: "../foo/bar.txt",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := &resolve.Scope{}
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
