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

func TestCopySchema_Resolver(t *testing.T) {
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
				sourceFileLists: map[string][]fs.SourceFile{
					"sources": {
						mustSourceFile(t, "//src/input.txt"),
					},
				},
				stringLists: map[string][]string{
					"outputs": {
						"{{source_file_part}}",
					},
				},
			}),
			want: DefaultMetadata{
				OutputPaths: []fs.OutputPath{
					mustOutputPath(t, "//out/Default/", "obj/{{source_file_part}}"),
				},
			},
			wantTools: []gotToolCall{
				{
					Tool:       "copy",
					Source:     mustSourceFile(t, "//src/input.txt"),
					OutputName: "{{source_file_part}}",
				},
			},
		},
		{
			name: "multisource",
			ctx: fakeResolverContext(fakeResolverData{
				sourceFileLists: map[string][]fs.SourceFile{
					"sources": {
						mustSourceFile(t, "//src/a.txt"),
						mustSourceFile(t, "//src/b.txt"),
					},
				},
				stringLists: map[string][]string{
					"outputs": {"gen/{{source_file_part}}"},
				},
			}),
			want: DefaultMetadata{
				OutputPaths: []fs.OutputPath{
					mustOutputPath(t, "//out/Default/", "obj/gen/{{source_file_part}}"),
					mustOutputPath(t, "//out/Default/", "obj/gen/{{source_file_part}}"),
				},
			},
			wantTools: []gotToolCall{
				{
					Tool:       "copy",
					Source:     mustSourceFile(t, "//src/a.txt"),
					OutputName: "gen/{{source_file_part}}",
				},
				{
					Tool:       "copy",
					Source:     mustSourceFile(t, "//src/b.txt"),
					OutputName: "gen/{{source_file_part}}",
				},
			},
		},
		{
			name: "nosources",
			ctx: fakeResolverContext(fakeResolverData{
				sourceFileLists: map[string][]fs.SourceFile{},
				stringLists: map[string][]string{
					"outputs": {"out.txt"},
				},
			}),
			wantErr: &CopyNoSourcesError{},
		},
		{
			name: "nooutputs",
			ctx: fakeResolverContext(fakeResolverData{
				sourceFileLists: map[string][]fs.SourceFile{
					"sources": {mustSourceFile(t, "//src/input.txt")},
				},
				stringLists: map[string][]string{},
			}),
			wantErr: &CopyBadOutputsError{},
		},
		{
			name: "multioutputs",
			ctx: fakeResolverContext(fakeResolverData{
				sourceFileLists: map[string][]fs.SourceFile{
					"sources": {mustSourceFile(t, "//src/input.txt")},
				},
				stringLists: map[string][]string{
					"outputs": {"out1.txt", "out2.txt"},
				},
			}),
			wantErr: &CopyBadOutputsError{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotTools []gotToolCall
			tc.ctx.DeclareTool = func(tool string, source fs.SourceFile, inputs []fs.SourceFile, outputName string, expansions map[string]string) (fs.OutputPath, error) {
				gotTools = append(gotTools, gotToolCall{
					Tool:       tool,
					Source:     source,
					Inputs:     inputs,
					OutputName: outputName,
					Expansions: expansions,
				})
				return fs.MakeOutputPath(mustSourceDir(t, "//out/Default/"), "obj/"+outputName), nil
			}

			got, err := CopySchema.Resolver(tc.ctx)

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
