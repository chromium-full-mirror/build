// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/analysis"
	"go.chromium.org/build/gong/gn/build/environment"
)

func TestWriteToolchain(t *testing.T) {
	tc := analysis.NewToolchain(environment.Label{Name: "gcc"}, nil)
	tc.Tools = map[string]*analysis.Tool{
		"cc": {
			Name: "cc",
			// Simple string for now until pattern substitution implemented.
			Command:     "gcc -c ${in} -o ${out}",
			Description: "CC ${out}",
		},
		"alink": {
			Name:        "alink",
			Command:     "ar rcs ${out} ${in}",
			Description: "AR ${out}",
		},
	}
	want := `rule alink
  command = ar rcs ${out} ${in}
  description = AR ${out}

rule cc
  command = gcc -c ${in} -o ${out}
  description = CC ${out}

`

	var buf bytes.Buffer
	err := WriteToolchain(&buf, tc)
	if err != nil {
		t.Fatalf("WriteToolchain()=%v; want nil err", err)
	}

	if diff := cmp.Diff(buf.String(), want); diff != "" {
		t.Errorf("WriteToolchain(); diff (-want +got):\n%s", diff)
	}
}
