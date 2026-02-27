// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

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
				},
				{
					Tool: "link",
					Inputs: []fs.SourceFile{
						mustFile(t, "//out/Default/obj/base/main.o"),
						mustFile(t, "//out/Default/obj/foo/libfoo.o"),
					},
					Output: mustFile(t, "//out/Default/obj/base/app"),
				},
			},
			want: `build obj/base/main.o: cxx ../../base/main.cc
build obj/base/app: link obj/base/main.o obj/foo/libfoo.o
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
						mustFile(t, "//out/Default/obj/other_dep.rlib"),
					},
					Output: mustFile(t, "//out/Default/obj/libfoo.rlib"),
				},
			},
			want: `build obj/libfoo.rlib: rust_rlib ../../src/lib.rs | obj/other_dep.rlib
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

			var sb strings.Builder
			if err := WriteTarget(&sb, target, mustDir(t, "//out/Default/")); err != nil {
				t.Fatalf("WriteTarget()=%v; want nil err", err)
			}

			if diff := cmp.Diff(tc.want, sb.String()); diff != "" {
				t.Errorf("WriteTarget() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
