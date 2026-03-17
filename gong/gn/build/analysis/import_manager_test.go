// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"errors"
	"testing"
	"testing/fstest"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

func TestImportManager_Success(t *testing.T) {
	files := fstest.MapFS{
		"foo.gni": &fstest.MapFile{
			Data: []byte(`
a = 42
_b = "private"
`),
		},
	}
	settings := NewSettings(
		&environment.BuildSettings{},
		NewImportManager(&fs.InputFileManager{FS: files}),
	)

	file, _ := fs.MakeSourceFile("//foo.gni")
	dest := settings.NewScope()
	err := settings.importManager.DoImport(file, &parse.LiteralNode{}, dest, settings)
	if err != nil {
		t.Fatalf("DoImport(%v, _)=%v; want nil err", file, err)
	}

	if v := dest.Value("a", false); v == nil {
		t.Fatalf("dest.a = nil; want non-nil")
	} else if v.RawGNString() != "42" {
		t.Fatalf("dest.a = %v; want 42", v.RawGNString())
	}
	if v := dest.Value("_b", false); v != nil {
		t.Fatalf("dest._b != nil; want nil")
	}

	// Values should automatically be marked as used in the destination scope
	if err := dest.CheckForUnusedVars(); err != nil {
		t.Errorf("dest.CheckForUnusedVars()=%v; want nil", err)
	}
}

func TestImportManager_CacheSuccess(t *testing.T) {
	files := fstest.MapFS{
		"foo.gni": &fstest.MapFile{
			Data: []byte(`a = 42`),
		},
	}
	settings := NewSettings(
		&environment.BuildSettings{},
		NewImportManager(&fs.InputFileManager{FS: files}),
	)
	file, _ := fs.MakeSourceFile("//foo.gni")
	err := settings.importManager.DoImport(file, &parse.LiteralNode{}, settings.NewScope(), settings)
	if err != nil {
		t.Fatalf("DoImport(%v, _)=%v; want nil", file, err)
	}

	files["foo.gni"] = &fstest.MapFile{
		// Modify foo.gni to prove second import doesn't re-read the file.
		Data: []byte("a = 2"),
	}
	dest := settings.NewScope()
	err = settings.importManager.DoImport(file, &parse.LiteralNode{}, dest, settings)
	if err != nil {
		t.Fatalf("DoImport(%v, _)=%v; want nil", file, err)
	}

	if v := dest.Value("a", false); v == nil {
		t.Fatalf("dest.a = nil; want non-nil")
	} else if v.RawGNString() != "42" {
		t.Fatalf("dest.a = %v; want 42", v.RawGNString())
	}
}

func TestImportManager_ImportLoop(t *testing.T) {
	files := fstest.MapFS{
		"foo.gni": &fstest.MapFile{
			Data: []byte(`import("//bar.gni")`),
		},
		"bar.gni": &fstest.MapFile{
			Data: []byte(`import("//foo.gni")`),
		},
	}

	settings := NewSettings(
		&environment.BuildSettings{},
		NewImportManager(&fs.InputFileManager{FS: files}),
	)
	file, _ := fs.MakeSourceFile("//foo.gni")
	err := settings.importManager.DoImport(file, &parse.LiteralNode{}, settings.NewScope(), settings)

	if e, ok := errors.AsType[*ImportError](err); !ok {
		t.Fatalf("DoImport(%v, _)=%v (%T); want ImportError", file, err, err)
	} else if _, ok := errors.AsType[*ImportLoopError](e.stack[0]); !ok {
		t.Fatalf("import err cause=%v (%T); want ImportLoopError", e.stack[0], e.stack[0])
	}
}

func TestImportManager_CacheError(t *testing.T) {
	files := fstest.MapFS{
		"foo.gni": &fstest.MapFile{Data: []byte(`a = undefined_variable`)},
	}
	settings := NewSettings(
		&environment.BuildSettings{},
		NewImportManager(&fs.InputFileManager{FS: files}),
	)

	file, _ := fs.MakeSourceFile("//foo.gni")
	err := settings.importManager.DoImport(file, &parse.LiteralNode{}, settings.NewScope(), settings)
	var actualErr resolve.UndefinedIdentifierError
	if e, ok := errors.AsType[*ImportError](err); !ok {
		t.Fatalf("DoImport(%v, _)=%v (%T); want ImportError", file, err, err)
	} else if !errors.As(e.stack[0], &actualErr) {
		t.Fatalf("import err cause=%v (%T); want UndefinedIdentifierError", e.stack[0], e.stack[0])
	}

	files["foo.gni"] = &fstest.MapFile{
		// Modify foo.gni to prove second import doesn't re-read the file.
		Data: []byte("a = 2"),
	}
	err = settings.importManager.DoImport(file, &parse.LiteralNode{}, settings.NewScope(), settings)
	if e, ok := errors.AsType[*ImportError](err); !ok {
		t.Fatalf("DoImport(%v, _)=%v (%T); want ImportError", file, err, err)
	} else if e.stack[0] != actualErr { //nolint:errorlint // Want exact error.
		t.Fatalf("import err cause=%v (%T); want same err %v (%T)", e.stack[0], e.stack[0], actualErr, actualErr)
	}
}
