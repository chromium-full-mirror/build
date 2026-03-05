// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package schemas

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

// TODO: possible to merge with TestStaticLibrarySchema_Resolver and TestRustLibrarySchema_Resolver?
// too much boilerplate duplicated between all three tests.
// Can combine into one table-based test because all resolvers implement metadata interface
// and can reuse gotToolCall for all cases?
// Major issue is that rust does not return DefaultMetadata, but can be indirectly tested through
// checking result of dep on rust?
func TestExecutableSchema_Resolver(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ctx       graph.ResolverContext
		want      DefaultMetadata
		wantTools []gotToolCall
		wantErr   any
	}{
		{
			name: "cxxsimple",
			ctx: fakeResolverContext(fakeResolverData{
				configValues: graph.ConfigValues{
					Cflags:  []string{"-O3", "-Wall"},
					Ldflags: []string{"-static", "-lpthread"},
				},
				strings: map[string]string{
					"name": "foo",
				},
				sourceFileLists: map[string][]fs.SourceFile{
					"sources": {
						mustSourceFile(t, "//src/main.cc"),
						mustSourceFile(t, "//src/util.h"),
					},
				},
				resolvedDeps: []graph.Resolution{
					{
						Metadata: DefaultMetadata{
							OutputFiles: []fs.SourceFile{
								mustSourceFile(t, "//out/obj/libbar.a"),
							},
						},
					},
				},
			}),
			want: DefaultMetadata{
				OutputFiles: []fs.SourceFile{
					mustSourceFile(t, "//out/obj/foo"),
				},
			},
			wantTools: []gotToolCall{
				{
					Tool:       "cxx",
					Source:     mustSourceFile(t, "//src/main.cc"),
					Inputs:     []fs.SourceFile{mustSourceFile(t, "//src/main.cc")},
					OutputName: "foo.main.cc.o",
					Expansions: map[string]string{
						"source_file_part": "",
						"source_name_part": "",
						"cflags":           "-O3 -Wall",
					},
				},
				{
					Tool:   "link",
					Source: fs.SourceFile{},
					Inputs: []fs.SourceFile{
						mustSourceFile(t, "//out/obj/foo.main.cc.o"),
						mustSourceFile(t, "//out/obj/libbar.a"),
					},
					OutputName: "foo",
					Expansions: map[string]string{
						"ldflags":      "-static -lpthread",
						"libs":         "",
						"frameworks":   "",
						"swiftmodules": "",
					},
				},
			},
		},
		{
			name: "cxxnosources",
			ctx: fakeResolverContext(fakeResolverData{
				strings: map[string]string{
					"name": "foo",
				},
				resolvedDeps: []graph.Resolution{
					{
						Metadata: DefaultMetadata{
							OutputFiles: []fs.SourceFile{
								mustSourceFile(t, "//out/obj/libbar.a"),
								mustSourceFile(t, "//out/obj/libbaz.a"),
							},
						},
					},
				},
			}),
			want: DefaultMetadata{
				OutputFiles: []fs.SourceFile{
					mustSourceFile(t, "//out/obj/foo"),
				},
			},
			wantTools: []gotToolCall{
				{
					Tool:   "link",
					Source: fs.SourceFile{},
					Inputs: []fs.SourceFile{
						mustSourceFile(t, "//out/obj/libbar.a"),
						mustSourceFile(t, "//out/obj/libbaz.a"),
					},
					OutputName: "foo",
					Expansions: map[string]string{
						"ldflags":      "",
						"libs":         "",
						"frameworks":   "",
						"swiftmodules": "",
					},
				},
			},
		},
		{
			name: "rustsimple",
			ctx: fakeResolverContext(fakeResolverData{
				configValues: graph.ConfigValues{
					Rustflags: []string{"-Cdebuginfo=2", "--edition=2021"},
				},
				strings: map[string]string{
					"name":       "foo_app",
					"crate_name": "foo_crate",
				},
				sourceFileLists: map[string][]fs.SourceFile{
					"sources": {
						mustSourceFile(t, "//src/main.rs"),
						mustSourceFile(t, "//src/util.rs"),
					},
				},
				resolvedDeps: []graph.Resolution{
					{
						Metadata: RustLibraryMetadata{
							CrateName:  "bar",
							OutputRlib: mustSourceFile(t, "//out/obj/libbar.rlib"),
							TransitiveRlibs: []fs.SourceFile{
								mustSourceFile(t, "//out/obj/libbaz.rlib"),
							},
						},
					},
				},
			}),
			want: DefaultMetadata{
				OutputFiles: []fs.SourceFile{
					mustSourceFile(t, "//out/obj/foo_crate"),
				},
			},
			wantTools: []gotToolCall{
				{
					Tool:   "rust_bin",
					Source: mustSourceFile(t, "//src/main.rs"),
					Inputs: []fs.SourceFile{
						mustSourceFile(t, "//src/main.rs"),
						mustSourceFile(t, "//src/util.rs"),
						mustSourceFile(t, "//out/obj/libbar.rlib"),
						mustSourceFile(t, "//out/obj/libbaz.rlib"),
					},
					OutputName: "foo_crate",
					Expansions: map[string]string{
						"crate_name": "foo_crate",
						"crate_type": "bin",
						"externs":    "--extern bar=//out/obj/libbar.rlib",
						"rustflags":  "-Cdebuginfo=2 --edition=2021",
						"rustdeps":   "",
					},
				},
			},
		},
		{
			name: "rustnosource",
			ctx: fakeResolverContext(fakeResolverData{
				strings: map[string]string{
					"name":       "foo_app",
					"crate_name": "foo_crate",
				},
				sourceFiles: map[string]fs.SourceFile{
					"crate_root": mustSourceFile(t, "//src/foo_root.rs"),
				},
				resolvedDeps: []graph.Resolution{
					{
						Metadata: RustLibraryMetadata{
							CrateName:  "bar",
							OutputRlib: mustSourceFile(t, "//out/obj/libbar.rlib"),
							TransitiveRlibs: []fs.SourceFile{
								mustSourceFile(t, "//out/obj/libbaz.rlib"),
							},
						},
					},
				},
			}),
			want: DefaultMetadata{
				OutputFiles: []fs.SourceFile{
					mustSourceFile(t, "//out/obj/foo_crate"),
				},
			},
			wantTools: []gotToolCall{
				{
					Tool:   "rust_bin",
					Source: mustSourceFile(t, "//src/foo_root.rs"),
					Inputs: []fs.SourceFile{
						mustSourceFile(t, "//out/obj/libbar.rlib"),
						mustSourceFile(t, "//out/obj/libbaz.rlib"),
					},
					OutputName: "foo_crate",
					Expansions: map[string]string{
						"crate_name": "foo_crate",
						"crate_type": "bin",
						"externs":    "--extern bar=//out/obj/libbar.rlib",
						"rustflags":  "",
						"rustdeps":   "",
					},
				},
			},
		},
		{
			name: "mixederror",
			ctx: fakeResolverContext(fakeResolverData{
				strings: map[string]string{
					"name": "foo",
				},
				sourceFileLists: map[string][]fs.SourceFile{
					"sources": {
						mustSourceFile(t, "//src/main.cc"),
						mustSourceFile(t, "//src/lib.rs"),
					},
				},
			}),
			wantErr: &BinaryMixedSourcesError{},
		},
		{
			name: "invaliderror",
			ctx: fakeResolverContext(fakeResolverData{
				strings: map[string]string{
					"name": "foo",
				},
				sourceFileLists: map[string][]fs.SourceFile{
					"sources": {
						mustSourceFile(t, "//src/readme.txt"),
					},
				},
			}),
			wantErr: &BinaryInvalidSourceError{
				targetName: "executable",
				sourceName: "//src/readme.txt",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotTools []gotToolCall
			tc.ctx.DeclareTool = func(tool string, source fs.SourceFile, inputs []fs.SourceFile, outputName string, expansions map[string]string) (fs.SourceFile, error) {
				gotTools = append(gotTools, gotToolCall{
					Tool:       tool,
					Source:     source,
					Inputs:     inputs,
					OutputName: outputName,
					Expansions: expansions,
				})
				return mustSourceFile(t, "//out/obj/"+outputName), nil
			}

			got, err := ExecutableSchema.Resolver(tc.ctx)

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
				t.Errorf("Resolver() metadata diff (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantTools, gotTools); diff != "" {
				t.Errorf("Resolver() DeclareTool calls mismatch; diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStaticLibrarySchema_Resolver(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ctx       graph.ResolverContext
		want      DefaultMetadata
		wantTools []gotToolCall
		wantErr   any
	}{
		{
			name: "simple",
			ctx: fakeResolverContext(fakeResolverData{
				configValues: graph.ConfigValues{
					Cflags:  []string{"-fPIC", "-O2"},
					Arflags: []string{"rcs"},
				},
				strings: map[string]string{
					"name": "bar",
				},
				sourceFileLists: map[string][]fs.SourceFile{
					"sources": {
						mustSourceFile(t, "//src/lib.cc"),
					},
				},
			}),
			want: DefaultMetadata{
				OutputFiles: []fs.SourceFile{
					mustSourceFile(t, "//out/obj/libbar.a"),
				},
			},
			wantTools: []gotToolCall{
				{
					Tool:       "cxx",
					Source:     mustSourceFile(t, "//src/lib.cc"),
					Inputs:     []fs.SourceFile{mustSourceFile(t, "//src/lib.cc")},
					OutputName: "libbar.lib.cc.o",
					Expansions: map[string]string{
						"source_file_part": "",
						"source_name_part": "",
						"cflags":           "-fPIC -O2",
					},
				},
				{
					Tool:   "alink",
					Source: fs.SourceFile{},
					Inputs: []fs.SourceFile{
						mustSourceFile(t, "//out/obj/libbar.lib.cc.o"),
					},
					OutputName: "libbar.a",
					Expansions: map[string]string{
						"arflags": "rcs",
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotTools []gotToolCall
			tc.ctx.DeclareTool = func(tool string, source fs.SourceFile, inputs []fs.SourceFile, outputName string, expansions map[string]string) (fs.SourceFile, error) {
				gotTools = append(gotTools, gotToolCall{
					Tool:       tool,
					Source:     source,
					Inputs:     inputs,
					OutputName: outputName,
					Expansions: expansions,
				})
				return mustSourceFile(t, "//out/obj/"+outputName), nil
			}

			got, err := StaticLibrarySchema.Resolver(tc.ctx)

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
				t.Errorf("Resolver() metadata diff (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantTools, gotTools); diff != "" {
				t.Errorf("Resolver() DeclareTool calls mismatch; diff (-want +got):\n%s", diff)
			}
		})
	}
}
