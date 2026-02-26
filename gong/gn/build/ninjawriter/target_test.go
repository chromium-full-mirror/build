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
					Inputs: []fs.SourceFile{mustFile(t, "//src/main.cc")},
					Output: mustFile(t, "//obj/src/main.o"),
				},
				{
					Tool: "link",
					Inputs: []fs.SourceFile{
						mustFile(t, "//obj/src/main.o"),
						mustFile(t, "//obj/src/libfoo.o"),
					},
					Output: mustFile(t, "//bin/app"),
				},
			},
			want: `build FAKEPATH//obj/src/main.o: cxx FAKEPATH//src/main.cc
build FAKEPATH//bin/app: link FAKEPATH//obj/src/main.o FAKEPATH//obj/src/libfoo.o
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
						mustFile(t, "//obj/other_dep.rlib"),
						mustFile(t, "//obj/transitive_dep.rlib"),
					},
					Output: mustFile(t, "//obj/libfoo.rlib"),
				},
			},
			want: `build FAKEPATH//obj/libfoo.rlib: rust_rlib FAKEPATH//src/lib.rs | FAKEPATH//obj/other_dep.rlib FAKEPATH//obj/transitive_dep.rlib
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
			if err := WriteTarget(&sb, target); err != nil {
				t.Fatalf("WriteTarget()=%v; want nil err", err)
			}

			if diff := cmp.Diff(tc.want, sb.String()); diff != "" {
				t.Errorf("WriteTarget() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
