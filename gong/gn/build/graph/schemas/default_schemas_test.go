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
	cmpOpts := []cmp.Option{
		cmp.Comparer(func(x, y fs.SourceFile) bool {
			if x == (fs.SourceFile{}) || y == (fs.SourceFile{}) {
				return x == y
			}
			return x.Filename() == y.Filename()
		}),
	}

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
				},
				{
					Tool:   "link",
					Source: fs.SourceFile{},
					Inputs: []fs.SourceFile{
						mustSourceFile(t, "//out/obj/foo.main.cc.o"),
					},
					OutputName: "foo",
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotTools []gotToolCall
			tc.ctx.DeclareTool = func(tool string, source fs.SourceFile, inputs []fs.SourceFile, outputName string) (fs.SourceFile, error) {
				gotTools = append(gotTools, gotToolCall{
					Tool:       tool,
					Source:     source,
					Inputs:     inputs,
					OutputName: outputName,
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

			if diff := cmp.Diff(tc.want, got, cmpOpts...); diff != "" {
				t.Errorf("Resolver() metadata diff (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantTools, gotTools, cmpOpts...); diff != "" {
				t.Errorf("Resolver() DeclareTool calls mismatch; diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStaticLibrarySchema_Resolver(t *testing.T) {
	cmpOpts := []cmp.Option{
		cmp.Comparer(func(x, y fs.SourceFile) bool {
			if x == (fs.SourceFile{}) || y == (fs.SourceFile{}) {
				return x == y
			}
			return x.Filename() == y.Filename()
		}),
	}

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
				},
				{
					Tool:   "alink",
					Source: fs.SourceFile{},
					Inputs: []fs.SourceFile{
						mustSourceFile(t, "//out/obj/libbar.lib.cc.o"),
					},
					OutputName: "libbar.a",
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotTools []gotToolCall
			tc.ctx.DeclareTool = func(tool string, source fs.SourceFile, inputs []fs.SourceFile, outputName string) (fs.SourceFile, error) {
				gotTools = append(gotTools, gotToolCall{
					Tool:       tool,
					Source:     source,
					Inputs:     inputs,
					OutputName: outputName,
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

			if diff := cmp.Diff(tc.want, got, cmpOpts...); diff != "" {
				t.Errorf("Resolver() metadata diff (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantTools, gotTools, cmpOpts...); diff != "" {
				t.Errorf("Resolver() DeclareTool calls mismatch; diff (-want +got):\n%s", diff)
			}
		})
	}
}
