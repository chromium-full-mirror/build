// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/analysis"
	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
	"go.chromium.org/build/gong/gn/build/ninjawriter"
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

// tempDir returns real path of temp dir.
// mac uses /tmp -> private/tmp symlink, so TempDir may contain
// symlink in the path. Using EvalSymlinks makes it the real path.
func tempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// stripLeadingNewline strips first newline, so improve readability of test cases.
//
// So instead of:
//
//			"obj/foo.ninja": `output_extension = .so
//	output_dir = obj
//	target_output_name = libfoo
//	target_out_dir = obj
//	`
//
// Can write instead:
//
//			"obj/foo.ninja": `
//	output_extension = .so
//	output_dir = obj
//	target_output_name = libfoo
//	target_out_dir = obj
//	`
func stripLeadingNewline(s string) string {
	return strings.TrimPrefix(s, "\n")
}

func setupFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for pathStr, content := range files {
		content = stripLeadingNewline(content)
		p := filepath.Join(dir, pathStr)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func runTest(t *testing.T, files map[string]string, wantNinja map[string]string) {
	dir := tempDir(t)
	setupFiles(t, dir, files)

	outDir, err := fs.MakeSourceDir("//out/Default/")
	if err != nil {
		t.Fatal(err)
	}

	buildSettings := &environment.BuildSettings{
		RootPath:        dir,
		BuildDir:        outDir,
		BuildConfigFile: mustFile(t, "//build/BUILDCONFIG.gn"),
	}

	loader := analysis.MakeLoader(buildSettings, &fs.InputFileManager{})
	builder := analysis.MakeBuilder(&loader)

	items, err := loader.Load(mustFile(t, "//BUILD.gn"), syntax.LocationRange{}, environment.Label{})
	if err != nil {
		t.Fatalf("loader.Load() = %v; want nil err", err)
	}

	var resolvedItems []graph.Item
	for _, item := range items {
		_, resolved, err := builder.RecordDefinedItem(item)
		if err != nil {
			t.Fatalf("builder.RecordDefinedItem() = %v; want nil err", err)
		}
		resolvedItems = append(resolvedItems, resolved...)
	}

	toolchains := make(map[environment.Label]*graph.Toolchain)
	targetsByTC := make(map[environment.Label][]*graph.Target)

	for _, item := range resolvedItems {
		switch v := item.(type) {
		case *graph.Toolchain:
			toolchains[v.Label()] = v
		case *graph.Target:
			tcLabel := v.Label().ToolchainLabel()
			targetsByTC[tcLabel] = append(targetsByTC[tcLabel], v)
		}
	}

	err = ninjawriter.Write(toolchains, targetsByTC, buildSettings)
	if err != nil {
		t.Fatalf("ninjawriter.Write() = %v; want nil err", err)
	}

	for wantPath, wantContent := range wantNinja {
		wantContent = stripLeadingNewline(wantContent)
		absPath := filepath.Join(dir, "out/Default", wantPath)
		gotBytes, err := os.ReadFile(absPath)
		if err != nil {
			t.Fatalf("os.ReadFile(%q) err = %v; want nil", wantPath, err)
		}

		if diff := cmp.Diff(wantContent, string(gotBytes)); diff != "" {
			t.Errorf("ninja file %s mismatch (-want +got):\n%s", wantPath, diff)
		}
	}
}
