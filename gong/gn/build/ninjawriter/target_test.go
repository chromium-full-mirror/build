// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

func mustFile(t *testing.T, s string) fs.SourceFile {
	t.Helper()
	f, err := fs.MakeSourceFile(s)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func mustOutputPath(t *testing.T, buildDir fs.SourceDir, rel string) fs.OutputPath {
	t.Helper()
	return fs.MakeOutputPath(buildDir, rel)
}

func mustSourceDir(t *testing.T, path string) fs.SourceDir {
	t.Helper()
	d, err := fs.MakeSourceDir(path)
	if err != nil {
		t.Fatalf("failed to make source dir %q: %v", path, err)
	}
	return d
}

func TestWriteSubninjaFile(t *testing.T) {
	tests := []struct {
		name    string
		actions []graph.Action
		want    string
	}{
		{
			name: "twostep",
			actions: []graph.Action{
				graph.RunToolAction{
					Tool:   "cxx",
					Source: mustFile(t, "//base/main.cc"),
					Inputs: []fs.SourceFile{mustFile(t, "//base/main.cc")},
					Output: mustOutputPath(t, mustSourceDir(t, "//out/Default/"), "obj/base/main.o"),
					Expansions: &graph.SimpleExpansions{Elems: map[string]string{
						"source_file_part": "main.cc",
						"source_name_part": "main",
					}},
				},
				graph.RunToolAction{
					Tool: "link",
					Inputs: []fs.SourceFile{
						mustFile(t, "//out/Default/obj/base/main.o"),
						mustFile(t, "//out/Default/obj/foo/libfoo.o"),
					},
					Output: mustOutputPath(t, mustSourceDir(t, "//out/Default/"), "obj/base/app"),
					Expansions: &graph.SimpleExpansions{Elems: map[string]string{
						"ldflags":      "",
						"libs":         "",
						"frameworks":   "",
						"swiftmodules": "",
					}},
				},
			},
			want: `output_dir = obj
target_output_name = app
target_out_dir = obj

build obj/base/main.o: cxx ../../base/main.cc
  source_file_part = main.cc
  source_name_part = main
build obj/base/app: link obj/base/main.o obj/foo/libfoo.o
  frameworks =
  ldflags =
  libs =
  swiftmodules =
`,
		},
		{
			name: "implicitdeps",
			actions: []graph.Action{
				graph.RunToolAction{
					Tool:   "rust_rlib",
					Source: mustFile(t, "//src/lib.rs"),
					Inputs: []fs.SourceFile{
						mustFile(t, "//src/lib.rs"),
						mustFile(t, "//out/Default/obj/bar/libbar.rlib"),
					},
					Output: mustOutputPath(t, mustSourceDir(t, "//out/Default/"), "obj/libfoo.rlib"),
					Expansions: &graph.SimpleExpansions{Elems: map[string]string{
						"crate_name": "foo",
						"crate_type": "rlib",
						"rustflags":  "-Cdebuginfo=2",
						"rustdeps":   "-Ldependency=obj/bar",
						"externs":    "--extern bar=obj/bar/libbar.rlib",
					}},
				},
			},
			want: `output_extension = .rlib
output_dir = obj
target_output_name = libfoo
target_out_dir = obj

build obj/libfoo.rlib: rust_rlib ../../src/lib.rs | obj/bar/libbar.rlib
  crate_name = foo
  crate_type = rlib
  externs = --extern bar=obj/bar/libbar.rlib
  rustdeps = -Ldependency=obj/bar
  rustflags = -Cdebuginfo=2
`,
		},
		{
			name: "escaping",
			actions: []graph.Action{
				graph.RunToolAction{
					Tool:   "copy",
					Source: mustFile(t, "//src/file with spaces.txt"),
					Inputs: []fs.SourceFile{
						mustFile(t, "//src/file with spaces.txt"),
					},
					Output:     mustOutputPath(t, mustSourceDir(t, "//out/Default/"), "obj/out file with spaces.txt"),
					Expansions: nil,
				},
			},
			want: `output_extension = .txt
output_dir = obj
target_output_name = out$ file$ with$ spaces
target_out_dir = obj

build obj/out$ file$ with$ spaces.txt: copy ../../src/file$ with$ spaces.txt
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := &graph.Target{
				Resolution: graph.Resolution{
					Actions: tc.actions,
				},
			}

			bs := &environment.BuildSettings{
				RootPath: "/my/builddir/",
				BuildDir: mustSourceDir(t, "/my/builddir/out/Default"),
			}

			var sb strings.Builder
			if err := writeSubninjaFile(&sb, target, bs); err != nil {
				t.Fatalf("writeSubninjaFile()=%v; want nil err", err)
			}

			if diff := cmp.Diff(tc.want, sb.String()); diff != "" {
				t.Errorf("writeSubninjaFile() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWriteAction_ScriptAction(t *testing.T) {
	tests := []struct {
		name   string
		action graph.RunScriptAction
		want   string
	}{
		{
			name: "basic",
			action: graph.RunScriptAction{
				Script: mustFile(t, "//tools/script.py"),
				Args: []graph.SubstitutionPattern{
					{Pattern: []graph.SubstitutionPart{graph.SubstitutionLiteral{Literal: "--flag"}}},
					{Pattern: []graph.SubstitutionPart{graph.SubstitutionLiteral{Literal: "hello world"}}},
				},
				Inputs: []fs.SourceFile{mustFile(t, "//src/input.txt")},
				Outputs: []fs.OutputPath{
					mustOutputPath(t, mustSourceDir(t, "//out/Default/"), "obj/foo.out"),
				},
			},
			want: `rule _rule
  command = python3 ../../tools/script.py --flag hello$ world
  description = ACTION ` +
				// TODO: expose a way to create graph.Target with a label
				`
  restat = 1

build obj/foo.out: _rule | ../../tools/script.py ../../src/input.txt
`,
		},
		{
			name: "with depfile",
			action: graph.RunScriptAction{
				Script: mustFile(t, "//tools/script.py"),
				Args: []graph.SubstitutionPattern{
					{Pattern: []graph.SubstitutionPart{graph.SubstitutionLiteral{Literal: "-o"}}},
					{Pattern: []graph.SubstitutionPart{graph.SubstitutionLiteral{Literal: "outfile.txt"}}},
				},
				Inputs: []fs.SourceFile{mustFile(t, "//src/input.txt")},
				Outputs: []fs.OutputPath{
					mustOutputPath(t, mustSourceDir(t, "//out/Default/"), "outfile.txt"),
				},
				Depfile: "outfile.d",
			},
			want: `rule _rule
  command = python3 ../../tools/script.py -o outfile.txt
  description = ACTION ` +
				// TODO: expose a way to create graph.Target with a label
				`
  restat = 1
  depfile = outfile.d
  deps = gcc

build outfile.txt: _rule | ../../tools/script.py ../../src/input.txt
`,
		},
		{
			name: "substitutions",
			action: graph.RunScriptAction{
				Script: mustFile(t, "//tools/script.py"),
				Args: []graph.SubstitutionPattern{
					{Pattern: []graph.SubstitutionPart{
						graph.SubstitutionLiteral{Literal: "--in="},
						graph.SubstitutionVar{
							GN:    "{{source}}",
							Ninja: "${in}",
						},
					}},
					{Pattern: []graph.SubstitutionPart{
						graph.SubstitutionLiteral{Literal: "--name="},
						graph.SubstitutionVar{
							GN:    "{{target_output_name}}",
							Ninja: "${target_output_name}",
						},
						graph.SubstitutionLiteral{Literal: "."},
						graph.SubstitutionVar{
							GN:    "{{output_extension}}",
							Ninja: "${output_extension}",
						},
					}},
					{Pattern: []graph.SubstitutionPart{graph.SubstitutionLiteral{Literal: "--verbose"}}},
				},
				Inputs: []fs.SourceFile{mustFile(t, "//src/input.txt")},
				Outputs: []fs.OutputPath{
					mustOutputPath(t, mustSourceDir(t, "//out/Default/"), "outfile.txt"),
				},
			},
			want: `rule _rule
  command = python3 ../../tools/script.py --in=${in} --name=${target_output_name}.${output_extension} --verbose
  description = ACTION ` +
				// TODO: expose a way to create graph.Target with a label
				`
  restat = 1

build outfile.txt: _rule | ../../tools/script.py ../../src/input.txt
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := &graph.Target{}

			bs := &environment.BuildSettings{
				RootPath:   "/my/builddir/",
				BuildDir:   mustSourceDir(t, "/my/builddir/out/Default"),
				PythonPath: "python3",
			}

			var sb strings.Builder
			if err := writeAction(&sb, target, tc.action, bs); err != nil {
				t.Fatalf("writeAction()=%v; want nil err", err)
			}

			if diff := cmp.Diff(tc.want, sb.String()); diff != "" {
				t.Errorf("writeAction() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
