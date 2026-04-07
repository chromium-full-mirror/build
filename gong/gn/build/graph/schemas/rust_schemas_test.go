// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package schemas

import (
	"errors"
	"fmt"
	"iter"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

type gotToolCall struct {
	Tool       string
	Source     fs.SourceFile
	Inputs     []fs.SourceFile
	OutputName string
	Expansions graph.Expansions
}

// TODO: move into somewhere common (utils_test.go)?
type fakeResolverData struct {
	booleans            map[string]bool
	strings             map[string]string
	labelKeyedStringMap map[string]map[environment.Label]string
	sourceFiles         map[string]fs.SourceFile
	sourceFileLists     map[string][]fs.SourceFile
	stringLists         map[string][]string
	resolvedDeps        []graph.Resolution
	configValues        graph.ConfigValues
}

// TODO: move into somewhere common (utils_test.go)?
func fakeResolverContext(data fakeResolverData) graph.ResolverContext {
	return graph.ResolverContext{
		LabelKeyedStringMapFor: func(varName string) (map[environment.Label]string, error) {
			if v, ok := data.labelKeyedStringMap[varName]; ok {
				return v, nil
			}
			return nil, fmt.Errorf("unknown var %q", varName)
		},
		StringFor: func(varName string) (string, error) {
			if v, ok := data.strings[varName]; ok {
				return v, nil
			}
			return "", fmt.Errorf("unknown var %q", varName)
		},
		BoolFor: func(varName string) (bool, error) {
			if v, ok := data.booleans[varName]; ok {
				return v, nil
			}
			return false, fmt.Errorf("unknown var %q", varName)
		},
		SourceFileFor: func(varName string) (fs.SourceFile, error) {
			if v, ok := data.sourceFiles[varName]; ok {
				return v, nil
			}
			return fs.SourceFile{}, fmt.Errorf("unknown var %q", varName)
		},
		SourceFilesFor: func(varName string) iter.Seq2[fs.SourceFile, error] {
			return func(yield func(fs.SourceFile, error) bool) {
				if v, ok := data.sourceFileLists[varName]; ok {
					for _, file := range v {
						if !yield(file, nil) {
							return
						}
					}
					return
				}
				yield(fs.SourceFile{}, fmt.Errorf("unknown var %q", varName))
			}
		},
		StringsFor: func(varName string) iter.Seq2[string, error] {
			return func(yield func(string, error) bool) {
				if v, ok := data.stringLists[varName]; ok {
					for _, d := range v {
						if !yield(d, nil) {
							return
						}
					}
					return
				}
				yield("", fmt.Errorf("unknown var %q", varName))
			}
		},
		ResolvedTargetsFor: func(varName string) iter.Seq2[graph.Resolution, error] {
			return func(yield func(graph.Resolution, error) bool) {
				switch varName {
				case "deps":
					for _, res := range data.resolvedDeps {
						if !yield(res, nil) {
							return
						}
					}
				default:
					yield(graph.Resolution{}, fmt.Errorf("unknown var %q", varName))
				}
			}
		},
		ConfigValues: data.configValues,
	}
}

func TestRustLibrarySchema_Resolver(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ctx       graph.ResolverContext
		want      RustLibraryMetadata
		wantTools []gotToolCall
		wantErr   any
	}{
		{
			name: "simple",
			ctx: fakeResolverContext(fakeResolverData{
				configValues: graph.ConfigValues{
					Rustflags: []string{"--edition=2021", "-Copt-level=3"},
				},
				strings: map[string]string{
					"name": "foo",
				},
				sourceFileLists: map[string][]fs.SourceFile{
					"sources": {mustSourceFile(t, "//src/lib.rs")},
				},
			}),
			want: RustLibraryMetadata{
				CrateName:  "foo",
				OutputRlib: mustOutputPath(t, "//out/", "obj/libfoo.rlib"),
			},
			wantTools: []gotToolCall{
				// build libfoo.rlib: rust_rlib lib.rs
				{
					Tool:       "rust_rlib",
					Source:     mustSourceFile(t, "//src/lib.rs"),
					Inputs:     []fs.SourceFile{mustSourceFile(t, "//src/lib.rs")},
					OutputName: "libfoo.rlib",
					Expansions: &graph.SimpleExpansions{Elems: map[string]string{
						"crate_name": "foo",
						"crate_type": "rlib",
						"externs":    "",
						"rustflags":  "--edition=2021 -Copt-level=3",
						"rustdeps":   "",
					}},
				},
			},
		},
		{
			name: "multi",
			ctx: fakeResolverContext(fakeResolverData{
				strings: map[string]string{
					"name": "foo",
				},
				sourceFiles: map[string]fs.SourceFile{
					"crate_root": mustSourceFile(t, "//src/custom_root.rs"),
				},
				sourceFileLists: map[string][]fs.SourceFile{
					"sources": {
						mustSourceFile(t, "//src/custom_root.rs"),
						mustSourceFile(t, "//src/other_file.rs"),
					},
				},
			}),
			want: RustLibraryMetadata{
				CrateName:  "foo",
				OutputRlib: mustOutputPath(t, "//out/", "obj/libfoo.rlib"),
			},
			wantTools: []gotToolCall{
				// build libfoo.rlib: rust_rlib custom_root.rs | custom_root.rs other_file.rs
				{
					Tool:   "rust_rlib",
					Source: mustSourceFile(t, "//src/custom_root.rs"),
					Inputs: []fs.SourceFile{
						mustSourceFile(t, "//src/custom_root.rs"),
						mustSourceFile(t, "//src/other_file.rs"),
					},
					OutputName: "libfoo.rlib",
					Expansions: &graph.SimpleExpansions{Elems: map[string]string{
						"crate_name": "foo",
						"crate_type": "rlib",
						"externs":    "",
						"rustflags":  "",
						"rustdeps":   "",
					}},
				},
			},
		},
		{
			name: "transitivedep",
			ctx: fakeResolverContext(fakeResolverData{
				strings: map[string]string{
					"name": "foo",
				},
				sourceFileLists: map[string][]fs.SourceFile{
					"sources": {mustSourceFile(t, "//src/lib.rs")},
				},
				resolvedDeps: []graph.Resolution{
					{
						Metadata: RustLibraryMetadata{
							CrateName:  "bar",
							OutputRlib: mustOutputPath(t, "//out/", "obj/bar.rlib"),
							TransitiveRlibs: []fs.SourceFile{
								mustSourceFile(t, "//out/obj/baz.rlib"),
							},
						},
					},
				},
			}),
			want: RustLibraryMetadata{
				CrateName:  "foo",
				OutputRlib: mustOutputPath(t, "//out/", "obj/libfoo.rlib"),
				TransitiveRlibs: []fs.SourceFile{
					mustSourceFile(t, "//out/obj/bar.rlib"),
					mustSourceFile(t, "//out/obj/baz.rlib"),
				},
			},
			wantTools: []gotToolCall{
				// build libfoo.rlib: rust_rlib lib.rs | lib.rs obj/libbar.rlib obj/libbaz.rlib
				{
					Tool:   "rust_rlib",
					Source: mustSourceFile(t, "//src/lib.rs"),
					Inputs: []fs.SourceFile{
						mustSourceFile(t, "//src/lib.rs"),
						mustSourceFile(t, "//out/obj/bar.rlib"),
						mustSourceFile(t, "//out/obj/baz.rlib"),
					},
					OutputName: "libfoo.rlib",
					Expansions: &graph.SimpleExpansions{Elems: map[string]string{
						"crate_name": "foo",
						"crate_type": "rlib",
						"externs":    "--extern bar=obj/bar.rlib",
						"rustflags":  "",
						"rustdeps":   "",
					}},
				},
			},
		},
		{
			name: "badcrateroot",
			ctx: fakeResolverContext(fakeResolverData{
				strings: map[string]string{
					"name": "bad_crate",
				},
				sourceFileLists: map[string][]fs.SourceFile{
					"sources": {
						mustSourceFile(t, "//src/a.rs"),
						mustSourceFile(t, "//src/b.rs"),
					},
				},
			}),
			wantErr: &CrateRootNotFoundError{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotTools []gotToolCall
			tc.ctx.DeclareTool = func(tool string, source fs.SourceFile, inputs []fs.SourceFile, outputName string, expansions graph.Expansions) (fs.OutputPath, error) {
				gotTools = append(gotTools, gotToolCall{
					Tool:       tool,
					Source:     source,
					Inputs:     inputs,
					OutputName: outputName,
					Expansions: expansions,
				})
				return fs.MakeOutputPath(mustSourceDir(t, "//out/"), "obj/"+outputName), nil
			}
			got, err := RustLibrarySchema.Resolver(tc.ctx)

			wantErr := tc.wantErr != nil
			gotErr := err != nil

			if gotErr != wantErr {
				t.Fatalf("Resolver() got err=%v (%T), wantErr %T", err, err, tc.wantErr)
			}

			if gotErr {
				if !errors.As(err, tc.wantErr) {
					t.Errorf("Resolver() got err=%v (%T), wantErr %T", err, err, tc.wantErr)
				}
				return
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Resolver() diff (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantTools, gotTools); diff != "" {
				t.Errorf("Resolver() DeclareTool calls mismatch; diff (-want +got):\n%s", diff)
			}
		})
	}
}
