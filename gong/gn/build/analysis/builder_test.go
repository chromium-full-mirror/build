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
	// TODO: 3 means //lib:foo, //lib:bar, //:tc but wrong?
	// but //:tc is default toolchain, should mark as resolved immediately.
	if appRec.unresolvedDeps != 3 {
		t.Errorf("builder record //:app(//:tc) unresolvedDeps = %d; want 3", appRec.unresolvedDeps)
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
		// TODO: change dep to ":my_config" after implicit label parse implemented.
		"BUILD.gn": {
			Data: []byte(`
toolchain("tc") { tool("link") { command = "" } }
config("my_config") {}
executable("app") {
    # should fail - target dep on config not allowed!
    deps = [ "//:my_config" ]
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
