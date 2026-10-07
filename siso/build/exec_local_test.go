// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"testing"

	"go.chromium.org/build/siso/path"
)

// TestIsRequiredOutput verifies directory outputs are classified as required even though def.Outputs keeps trailing slashes while cmd.AllOutputs strips them.
func TestIsRequiredOutput(t *testing.T) {
	for _, tc := range []struct {
		name       string
		out        string
		defOutputs []string
		want       bool
	}{
		{
			name:       "file_output_required",
			out:        "obj/foo.o",
			defOutputs: []string{"obj/foo.o", "obj/bar.o"},
			want:       true,
		},
		{
			name:       "file_output_not_in_def",
			out:        "obj/baz.o",
			defOutputs: []string{"obj/foo.o", "obj/bar.o"},
			want:       false,
		},
		{
			name:       "dir_output_required_trailing_slash_in_def",
			out:        "obj/extracted",
			defOutputs: []string{"obj/extracted/"},
			want:       true,
		},
		{
			name:       "mixed_file_and_dir_outputs",
			out:        "obj/extracted",
			defOutputs: []string{"obj/repackaged.zip", "obj/extracted/"},
			want:       true,
		},
		{
			name:       "dir_output_not_in_def",
			out:        "obj/other",
			defOutputs: []string{"obj/extracted/"},
			want:       false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := isRequiredOutput(path.Path(tc.out), path.Paths(tc.defOutputs))
			if got != tc.want {
				t.Errorf("isRequiredOutput(%q, %v) = %v; want %v",
					tc.out, tc.defOutputs, got, tc.want)
			}
		})
	}
}
