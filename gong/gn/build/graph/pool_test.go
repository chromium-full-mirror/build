// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"testing"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
)

func mustDir(t *testing.T, path string) fs.SourceDir {
	t.Helper()
	d, err := fs.MakeSourceDir(path)
	if err != nil {
		t.Fatalf("failed to make source dir %q: %v", path, err)
	}
	return d
}

func TestPool_NinjaName(t *testing.T) {
	for _, tc := range []struct {
		name             string
		pool             Pool
		includeToolchain bool
		want             string
		wantErr          bool
	}{
		{
			name: "simple",
			pool: Pool{
				ItemInfo: ItemInfo{
					label: environment.Label{
						Dir:  mustDir(t, "//foo/bar/"),
						Name: "baz",
					},
				},
			},
			includeToolchain: false,
			want:             "foo_bar_baz",
		},
		{
			name: "root",
			pool: Pool{
				ItemInfo: ItemInfo{
					label: environment.Label{
						Dir:  mustDir(t, "//"),
						Name: "baz",
					},
				},
			},
			includeToolchain: false,
			want:             "baz",
		},
		{
			name: "with toolchain",
			pool: Pool{
				ItemInfo: ItemInfo{
					label: environment.Label{
						Dir:           mustDir(t, "//foo/"),
						Name:          "bar",
						ToolchainDir:  mustDir(t, "//tc/"),
						ToolchainName: "gcc",
					},
				},
			},
			includeToolchain: true,
			want:             "tc_gcc_foo_bar",
		},
		{
			name: "with toolchain at root",
			pool: Pool{
				ItemInfo: ItemInfo{
					label: environment.Label{
						Dir:           mustDir(t, "//"),
						Name:          "bar",
						ToolchainDir:  mustDir(t, "//"),
						ToolchainName: "gcc",
					},
				},
			},
			includeToolchain: true,
			want:             "gcc_bar",
		},
		{
			name: "error label dir system absolute",
			pool: Pool{
				ItemInfo: ItemInfo{
					label: environment.Label{
						Dir:  mustDir(t, "/abs/"),
						Name: "foo",
					},
				},
			},
			includeToolchain: false,
			wantErr:          true,
		},
		{
			name: "error toolchain dir system absolute",
			pool: Pool{
				ItemInfo: ItemInfo{
					label: environment.Label{
						Dir:           mustDir(t, "//foo/"),
						Name:          "bar",
						ToolchainDir:  mustDir(t, "/abs/"),
						ToolchainName: "gcc",
					},
				},
			},
			includeToolchain: true,
			wantErr:          true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.pool.NinjaName(tc.includeToolchain)
			if tc.wantErr {
				if err == nil {
					t.Errorf("NinjaName(%v) = %q, nil; want error", tc.includeToolchain, got)
				}
				return
			}
			if err != nil {
				t.Errorf("NinjaName(%v) failed: %v", tc.includeToolchain, err)
				return
			}
			if got != tc.want {
				t.Errorf("NinjaName(%v) = %q; want %q", tc.includeToolchain, got, tc.want)
			}
		})
	}
}
