// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package environment

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

// Helper to create a fs.SourceDir from a string we know should be valid, so fail the test if it fails.
func mustDir(t *testing.T, path string) fs.SourceDir {
	t.Helper()
	d, err := fs.MakeSourceDir(path)
	if err != nil {
		t.Fatalf("setup error: failed to make source dir %q: %v", path, err)
	}
	return d
}

func TestResolveLabel(t *testing.T) {
	cmpOpts := []cmp.Option{
		cmp.AllowUnexported(Label{}),
		cmp.Comparer(func(x, y fs.SourceDir) bool {
			return x.Path() == y.Path()
		}),
	}

	for _, tc := range []struct {
		name        string
		input       string
		wd          string
		toolchain   Label
		want        Label
		wantErrKind syntax.ErrKind
	}{
		{
			name:      "absolute",
			input:     "//foo/bar:baz",
			wd:        "//chrome/browser/",
			toolchain: Label{Dir: mustDir(t, "//t/"), name: "d"},
			want: Label{
				Dir:           mustDir(t, "//foo/bar/"),
				name:          "baz",
				toolchainDir:  mustDir(t, "//t/"),
				toolchainName: "d",
			},
		},
		{
			name:        "implicit target name not yet supported",
			input:       "//foo/bar",
			wd:          "//chrome/browser/",
			wantErrKind: syntax.ErrNotImplemented,
		},
		{
			name:        "implicit target dir not yet supported",
			input:       ":baz",
			wd:          "//chrome/browser/",
			toolchain:   Label{Dir: mustDir(t, "//t/"), name: "d"},
			wantErrKind: syntax.ErrNotImplemented,
		},
		{
			name:      "explicit toolchain",
			input:     "//foo:bar(//t:two)",
			wd:        "//chrome/browser/",
			toolchain: Label{Dir: mustDir(t, "//t/"), name: "d"},
			want: Label{
				Dir:           mustDir(t, "//foo/"),
				name:          "bar",
				toolchainDir:  mustDir(t, "//t/"),
				toolchainName: "two",
			},
		},
		{
			name:        "invalid toolchain format",
			input:       "//foo:bar(//t:two",
			wd:          "//chrome/browser/",
			toolchain:   Label{Dir: mustDir(t, "//t/"), name: "d"},
			wantErrKind: syntax.ErrInvalidFormat,
		},
		{
			name:        "toolchain in toolchain",
			input:       "//foo:bar(//t:two(//t2:t2))",
			wd:          "//chrome/browser/",
			toolchain:   Label{Dir: mustDir(t, "//t/"), name: "d"},
			wantErrKind: syntax.ErrInvalidFormat,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveLabel(mustDir(t, tc.wd), tc.toolchain, resolve.NewOriginlessStringValue(tc.input))
			wantErr := tc.wantErrKind != ""
			gotErr := err != nil

			if gotErr != wantErr {
				t.Fatalf("ResolveLabel(%q) got err=%v, wantErr=%v (kind %s)", tc.input, err, wantErr, tc.wantErrKind)
			}

			if gotErr {
				if match, gotErrKind := syntax.AsErrKind(err, tc.wantErrKind); match == nil {
					t.Fatalf("ResolveLabel(%q) got err=%v (kind %s), wantErrKind=%s", tc.input, err, gotErrKind, tc.wantErrKind)
				}
				return
			}

			if diff := cmp.Diff(tc.want, got, cmpOpts...); diff != "" {
				t.Errorf("ResolveLabel(%q) mismatch (-want +got):\n%s", tc.input, diff)
			}
		})
	}
}

func TestLabel_UserVisibleString(t *testing.T) {
	for _, tc := range []struct {
		name       string
		label      Label
		wantOmitTc string
		wantWithTc string
	}{
		{
			name: "label in root",
			label: Label{
				Dir:           mustDir(t, "//"),
				name:          "name",
				toolchainDir:  mustDir(t, "//t"),
				toolchainName: "tn",
			},
			wantOmitTc: "//:name",
			wantWithTc: "//:name(//t:tn)",
		},
		{
			name: "label in subdir",
			label: Label{
				Dir:           mustDir(t, "//dir"),
				name:          "name",
				toolchainDir:  mustDir(t, "//t"),
				toolchainName: "tn",
			},
			wantOmitTc: "//dir:name",
			wantWithTc: "//dir:name(//t:tn)",
		},
		{
			name: "toolchain dir is empty",
			label: Label{
				Dir:           mustDir(t, "//dir"),
				name:          "name",
				toolchainDir:  fs.SourceDir{},
				toolchainName: "tn",
			},
			wantOmitTc: "//dir:name",
			wantWithTc: "//dir:name()",
		},
		{
			name: "label dir is empty hence label is empty",
			label: Label{
				Dir:           fs.SourceDir{},
				name:          "name",
				toolchainDir:  fs.SourceDir{},
				toolchainName: "tn",
			},
			wantOmitTc: "",
			wantWithTc: "",
		},
		{
			name:       "empty label",
			label:      Label{},
			wantOmitTc: "",
			wantWithTc: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.label.UserVisibleString(false)
			if diff := cmp.Diff(tc.wantOmitTc, got); diff != "" {
				t.Errorf("UserVisibleString(false) mismatch (-want +got):\n%s", diff)
			}
			got = tc.label.UserVisibleString(true)
			if diff := cmp.Diff(tc.wantWithTc, got); diff != "" {
				t.Errorf("UserVisibleString(true) mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
