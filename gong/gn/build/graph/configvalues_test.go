// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/fs"
)

func mustSourceFile(t *testing.T, path string) fs.SourceFile {
	t.Helper()
	f, err := fs.MakeSourceFile(path)
	if err != nil {
		t.Fatalf("failed to make source file %q: %v", path, err)
	}
	return f
}

func TestMakeConfigValues(t *testing.T) {
	file1 := mustSourceFile(t, "//foo/file1.cc")
	want := ConfigValues{
		Cflags:  []string{"-O2", "-Wall"},
		Defines: []string{"FOO=1"},
		Inputs:  []fs.SourceFile{file1},
	}

	cv, err := MakeConfigValues(map[string]ProcessedValue{
		"cflags":  StringListValue{list: []string{"-O2", "-Wall"}},
		"defines": StringListValue{list: []string{"FOO=1"}},
		"inputs":  FileListValue{list: []fs.SourceFile{file1}},
	})
	if err != nil {
		t.Fatalf("MakeConfigValues(_)=_,%v; want nil err", err)
	}

	if diff := cmp.Diff(want, cv); diff != "" {
		t.Errorf("MakeConfigValues(_); (-want +got):\n%s", diff)
	}
}

func TestAppendConfigs(t *testing.T) {
	base := ConfigValues{
		Cflags: []string{"-O1"},
	}
	want := ConfigValues{
		Cflags:  []string{"-O1", "-O2"},
		Defines: []string{"DEP1=1", "DEP2=1"},
	}

	err := base.Append(
		&Config{
			resolvedValues: &ConfigValues{
				Cflags:  []string{"-O2"},
				Defines: []string{"DEP1=1"},
			},
		},
		&Config{
			resolvedValues: &ConfigValues{
				Defines: []string{"DEP2=1"},
			},
		},
	)
	if err != nil {
		t.Fatalf("Append(_)=%v; want nil err", err)
	}

	if diff := cmp.Diff(want, base); diff != "" {
		t.Errorf("Append(_); diff (-want +got):\n%s", diff)
	}
}

func TestAppendConfigs_EmptyAllocations(t *testing.T) {
	base := ConfigValues{
		Cflags:  []string{"-O2", "-Wall"},
		Defines: []string{"FOO=1"},
	}

	allocs := testing.AllocsPerRun(100, func() {
		err := base.Append(&Config{})
		if err != nil {
			t.Fatalf("Append(_)=%v; want nil err", err)
		}
	})

	if allocs != 0 {
		t.Errorf("allocs=%f; want 0", allocs)
	}
}
