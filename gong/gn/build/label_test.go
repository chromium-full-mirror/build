// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/fs"
)

func TestLabel_UserVisibleString(t *testing.T) {
	mustDir := func(s string) fs.SourceDir {
		d, err := fs.MakeSourceDir(s)
		if err != nil {
			t.Fatalf("MakeSourceDir(%q) failed: %v", s, err)
		}
		return d
	}

	for _, tc := range []struct {
		name       string
		label      Label
		wantOmitTc string
		wantWithTc string
	}{
		{
			name: "label in root",
			label: Label{
				dir:           mustDir("//"),
				name:          "name",
				toolchainDir:  mustDir("//t"),
				toolchainName: "tn",
			},
			wantOmitTc: "//:name",
			wantWithTc: "//:name(//t:tn)",
		},
		{
			name: "label in subdir",
			label: Label{
				dir:           mustDir("//dir"),
				name:          "name",
				toolchainDir:  mustDir("//t"),
				toolchainName: "tn",
			},
			wantOmitTc: "//dir:name",
			wantWithTc: "//dir:name(//t:tn)",
		},
		{
			name: "toolchain dir is empty",
			label: Label{
				dir:           mustDir("//dir"),
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
				dir:           fs.SourceDir{},
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
