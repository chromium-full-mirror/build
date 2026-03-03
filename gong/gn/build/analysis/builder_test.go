// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"errors"
	iofs "io/fs"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/syntax"
)

func mustDir(t *testing.T, path string) fs.SourceDir {
	t.Helper()
	d, err := fs.MakeSourceDir(path)
	if err != nil {
		t.Fatalf("failed to make source dir %q: %v", path, err)
	}
	return d
}

func mustFile(t *testing.T, s string) fs.SourceFile {
	t.Helper()
	f, err := fs.MakeSourceFile(s)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func tempBuildEnv(t *testing.T, testFS iofs.FS) (*Builder, *Loader) {
	t.Helper()
	buildDir, err := fs.MakeSourceDir("//out/Debug/")
	if err != nil {
		t.Fatal(err)
	}
	loader := MakeLoader(&environment.BuildSettings{
		BuildDir:        buildDir,
		BuildConfigFile: mustFile(t, "//build/BUILDCONFIG.gn"),
	}, &fs.InputFileManager{FS: testFS})
	builder := MakeBuilder(&loader)
	return &builder, &loader
}

func TestBuilder_Dependencies(t *testing.T) {
	builder, loader := tempBuildEnv(t, &fstest.MapFS{
		"build/BUILDCONFIG.gn": {
			Data: []byte(`
set_default_toolchain("//:tc")`),
		},
		"BUILD.gn": {
			Data: []byte(`
toolchain("tc") {
    tool("link") { command = "link" }
    tool("cxx") { command = "cc" }
}
config("my_config") {}
executable("app") {
    deps = [ "//lib:foo", "//lib:bar" ]
}`),
		},
		"lib/BUILD.gn": {
			Data: []byte(`
shared_library("foo") {}
shared_library("bar") {}`),
		},
	})

	items, err := loader.Load(mustFile(t, "//BUILD.gn"), syntax.LocationRange{}, environment.Label{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	for _, item := range items {
		_, err := builder.RecordDefinedItem(item)
		if err != nil {
			t.Fatalf("RecordDefinedItem(%s)=_, %v; want nil err", item.Label().UserVisibleString(true), err)
		}
	}

	appRec, ok := builder.records[environment.Label{
		Dir:           mustDir(t, "//"),
		Name:          "app",
		ToolchainDir:  mustDir(t, "//"),
		ToolchainName: "tc",
	}]
	if !ok {
		t.Fatal("builder missing record //:app(//:tc)")
	}
	if appRec.unresolvedDeps != 2 {
		t.Errorf("builder record //:app(//:tc) unresolvedDeps = %d; want 2", appRec.unresolvedDeps)
	}

	items, err = loader.Load(mustFile(t, "//lib/BUILD.gn"), syntax.LocationRange{}, environment.Label{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	for _, item := range items {
		_, err := builder.RecordDefinedItem(item)
		if err != nil {
			t.Fatalf("RecordDefinedItem(%s)=_, %v; want nil err", item.Label().UserVisibleString(true), err)
		}
	}

	wantRecords := []string{
		// Toolchain labels do not have a toolchain themselves.
		"//:tc()",
		// All other items should have a toolchain.
		"//:app(//:tc)",
		"//:my_config(//:tc)",
		"//lib:bar(//:tc)",
		"//lib:foo(//:tc)",
	}
	var gotRecords []string
	for label := range builder.records {
		gotRecords = append(gotRecords, label.UserVisibleString(true))
	}
	if diff := cmp.Diff(wantRecords, gotRecords, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
		t.Errorf("builder.records diff (-want +got):\n%s", diff)
	}
}

func TestBuilder_ItemTypeMismatch(t *testing.T) {
	builder, loader := tempBuildEnv(t, &fstest.MapFS{
		"build/BUILDCONFIG.gn": {
			Data: []byte(`
set_default_toolchain("//:tc")`),
		},
		"BUILD.gn": {
			Data: []byte(`
toolchain("tc") { tool("link") { command = "" } }
config("my_config") {}
executable("app") {
    # should fail - target dep on config not allowed!
    deps = [ ":my_config" ]
}`),
		},
	})
	items, err := loader.Load(mustFile(t, "//BUILD.gn"), syntax.LocationRange{}, environment.Label{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	var gotErr error
	for _, item := range items {
		_, err := builder.RecordDefinedItem(item)
		if err != nil {
			gotErr = err
			break
		}
	}

	var wantErr ItemTypeMismatchError
	if !errors.As(gotErr, &wantErr) {
		t.Errorf("builder record items finished with %T err; want %T err", gotErr, wantErr)
	}
}

func TestBuilder_ResolvesTargetsImmediatelyIfPossible(t *testing.T) {
	builder, loader := tempBuildEnv(t, &fstest.MapFS{
		"build/BUILDCONFIG.gn": {
			Data: []byte(`set_default_toolchain("//:tc")`),
		},
		"BUILD.gn": {
			Data: []byte(`
toolchain("tc") { tool("link") { command = "" } }
executable("app") {}`),
		},
	})
	items, err := loader.Load(mustFile(t, "//BUILD.gn"), syntax.LocationRange{}, environment.Label{})
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	for _, item := range items {
		_, err := builder.RecordDefinedItem(item)
		if err != nil {
			t.Fatalf("RecordDefinedItem failed: %v", err)
		}
	}

	appRec, ok := builder.records[environment.Label{
		Dir:           mustDir(t, "//"),
		Name:          "app",
		ToolchainDir:  mustDir(t, "//"),
		ToolchainName: "tc",
	}]
	if !ok {
		t.Fatalf("builder missing record for //:app(//:tc)")
	}
	if appRec.state != itemStateResolved {
		t.Errorf("builder record //:app(//:tc) state = %v; want %v (itemStateResolved)", appRec.state, itemStateResolved)
	}
}
