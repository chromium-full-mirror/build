// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/analysis"
	"go.chromium.org/build/gong/gn/build/fs"
)

func mustFile(t *testing.T, s string) fs.SourceFile {
	f, err := fs.MakeSourceFile(s)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestWriteTarget(t *testing.T) {
	target := &analysis.Target{
		Resolution: analysis.Resolution{
			Actions: []analysis.RunToolAction{
				{
					Tool:   "cxx",
					Inputs: []fs.SourceFile{mustFile(t, "//src/main.cc")},
					Output: mustFile(t, "//obj/src/main.o"),
				},
				{
					Tool:   "link",
					Inputs: []fs.SourceFile{mustFile(t, "//obj/src/main.o")},
					Output: mustFile(t, "//bin/app"),
				},
			},
			Output: mustFile(t, "//bin/app"),
		},
	}
	want := `build FAKEPATH//obj/src/main.o: cxx FAKEPATH//src/main.cc
build FAKEPATH//bin/app: link FAKEPATH//obj/src/main.o
`

	var buf bytes.Buffer
	if err := WriteTarget(&buf, target); err != nil {
		t.Fatalf("WriteTarget()=%v; want nil err", err)
	}

	if diff := cmp.Diff(buf.String(), want); diff != "" {
		t.Errorf("WriteTarget(); diff (-want +got):\n%s", diff)
	}
}
