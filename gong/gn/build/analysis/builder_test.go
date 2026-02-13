package analysis

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

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

// TODO: better for fs package to support virtual fs if other tests want to do this?
// check if C++ GN also uses virtual fs for anything?
func tempBuildEnv(t *testing.T, files map[string]string) (*Builder, *Loader) {
	t.Helper()

	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	buildDir, err := fs.MakeSourceDir("//out/Debug/")
	if err != nil {
		t.Fatal(err)
	}
	bs := &environment.BuildSettings{
		BuildDir:        buildDir,
		BuildConfigFile: mustFile(t, "//build/BUILDCONFIG.gn"),
	}
	bs.SetRootPath(dir)

	loader := MakeLoader(bs, &fs.InputFileManager{})
	builder := MakeBuilder(&loader)
	return &builder, &loader
}

func TestBuilder_Dependencies(t *testing.T) {
	builder, loader := tempBuildEnv(t, map[string]string{
		"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
		"BUILD.gn": `
toolchain("tc") {
    tool("link") { command = "link" }
    tool("cxx") { command = "cc" }
}
executable("app") {
    deps = [ "//lib:foo", "//lib:bar" ]
}`,
		"lib/BUILD.gn": `
shared_library("foo") {}
shared_library("bar") {}`,
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

	appRec, ok := builder.records[environment.Label{Dir: mustDir(t, "//"), Name: "app"}]
	if !ok {
		t.Fatal("builder missing record //:app")
	}
	// TODO: 3 means //lib:foo, //lib:bar, //:tc but wrong?
	// but //:tc is default toolchain, should mark as resolved immediately.
	if appRec.unresolvedDeps != 3 {
		t.Errorf("builder record //:app unresolvedDeps = %d; want 3", appRec.unresolvedDeps)
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

	if _, ok := builder.records[environment.Label{Dir: mustDir(t, "//lib/"), Name: "foo"}]; !ok {
		t.Fatal("builder missing record //lib:foo")
	}
	if _, ok := builder.records[environment.Label{Dir: mustDir(t, "//lib/"), Name: "bar"}]; !ok {
		t.Fatal("builder missing record //lib:bar")
	}
}

func TestBuilder_ItemTypeMismatch(t *testing.T) {
	builder, loader := tempBuildEnv(t, map[string]string{
		"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
		// TODO: change dep to ":my_config" after implicit label parse implemented.
		"BUILD.gn": `
toolchain("tc") { tool("link") { command = "" } }
config("my_config") {}
executable("app") {
    # should fail - target dep on config not allowed!
    deps = [ "//:my_config" ]
}`,
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
		t.Errorf("builder record items finished with %T err; want %v err", gotErr, wantErr)
	}
}
