// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

func mustFile(t *testing.T, s string) fs.SourceFile {
	t.Helper()
	f, err := fs.MakeSourceFile(s)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestWriteTarget(t *testing.T) {
	tests := []struct {
		name    string
		actions []graph.RunToolAction
		want    string
	}{
		{
			name: "twostep",
			actions: []graph.RunToolAction{
				{
					Tool:   "cxx",
					Source: mustFile(t, "//base/main.cc"),
					Inputs: []fs.SourceFile{mustFile(t, "//base/main.cc")},
					Output: mustFile(t, "//out/Default/obj/base/main.o"),
					Expansions: map[string]string{
						"source_file_part": "main.cc",
						"source_name_part": "main",
					},
				},
				{
					Tool: "link",
					Inputs: []fs.SourceFile{
						mustFile(t, "//out/Default/obj/base/main.o"),
						mustFile(t, "//out/Default/obj/foo/libfoo.o"),
					},
					Output: mustFile(t, "//out/Default/obj/base/app"),
					Expansions: map[string]string{
						"ldflags":      "",
						"libs":         "",
						"frameworks":   "",
						"swiftmodules": "",
					},
				},
			},
			want: `build obj/base/main.o: cxx ../../base/main.cc
  source_file_part = main.cc
  source_name_part = main
build obj/base/app: link obj/base/main.o obj/foo/libfoo.o
  frameworks =
  ldflags =
  libs =
  swiftmodules =
`,
		},
		{
			name: "implicitdeps",
			actions: []graph.RunToolAction{
				{
					Tool:   "rust_rlib",
					Source: mustFile(t, "//src/lib.rs"),
					Inputs: []fs.SourceFile{
						mustFile(t, "//src/lib.rs"),
						mustFile(t, "//out/Default/obj/bar/libbar.rlib"),
					},
					Output: mustFile(t, "//out/Default/obj/libfoo.rlib"),
					Expansions: map[string]string{
						"crate_name":     "foo",
						"crate_type":     "rlib",
						"target_out_dir": "obj/foo",
						"rustflags":      "-Cdebuginfo=2",
						"rustdeps":       "-Ldependency=obj/bar",
						"externs":        "--extern bar=obj/bar/libbar.rlib",
					},
				},
			},
			want: `build obj/libfoo.rlib: rust_rlib ../../src/lib.rs | obj/bar/libbar.rlib
  crate_name = foo
  crate_type = rlib
  externs = --extern bar=obj/bar/libbar.rlib
  rustdeps = -Ldependency=obj/bar
  rustflags = -Cdebuginfo=2
  target_out_dir = obj/foo
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := &graph.Target{
				Resolution: graph.Resolution{
					Actions: tc.actions,
				},
			}

			buildDir, _ := fs.MakeSourceDir("/my/builddir/out/Default")
			bs := &environment.BuildSettings{
				RootPath: "/my/builddir/",
				BuildDir: buildDir,
			}

			var sb strings.Builder
			if err := WriteTarget(&sb, target, bs); err != nil {
				t.Fatalf("WriteTarget()=%v; want nil err", err)
			}

			if diff := cmp.Diff(tc.want, sb.String()); diff != "" {
				t.Errorf("WriteTarget() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
