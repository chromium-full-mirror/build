// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package schemas

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

type gotScriptCall struct {
	Script         fs.SourceFile
	Args           []string
	OutputNames    []string
	Inputs         []fs.SourceFile
	Depfile        string
	RspfileContent []string
}

func TestActionSchema_Resolver(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ctx         graph.ResolverContext
		want        DefaultMetadata
		wantScripts []gotScriptCall
		wantErr     any
	}{
		{
			name: "simple",
			ctx: fakeResolverContext(fakeResolverData{
				sourceFiles: map[string]fs.SourceFile{
					"script": mustSourceFile(t, "//tools/myscript.py"),
				},
				sourceFileLists: map[string][]fs.SourceFile{
					"outputs": {
						mustSourceFile(t, "//out/Default/gen/output.txt"),
					},
					"sources": nil,
				},
				stringLists: map[string][]string{
					"args": nil,
				},
			}),
			want: DefaultMetadata{
				OutputPaths: []fs.OutputPath{
					mustOutputPath(t, "//out/Default/", "gen/output.txt"),
				},
			},
			wantScripts: []gotScriptCall{
				{
					Script: mustSourceFile(t, "//tools/myscript.py"),
					OutputNames: []string{
						"//out/Default/gen/output.txt",
					},
				},
			},
		},
		{
			name: "inputsargs",
			ctx: fakeResolverContext(fakeResolverData{
				sourceFiles: map[string]fs.SourceFile{
					"script": mustSourceFile(t, "//tools/myscript.py"),
				},
				stringLists: map[string][]string{
					"args": {"--foo", "bar"},
				},
				sourceFileLists: map[string][]fs.SourceFile{
					"inputs": {
						mustSourceFile(t, "//src/input1.txt"),
						mustSourceFile(t, "//src/input2.txt"),
					},
					"outputs": {
						mustSourceFile(t, "//out/Default/gen/output1.txt"),
						mustSourceFile(t, "//out/Default/gen/output2.txt"),
					},
				},
				strings: map[string]string{
					"depfile": "my_depfile.d",
				},
			}),
			want: DefaultMetadata{
				OutputPaths: []fs.OutputPath{
					mustOutputPath(t, "//out/Default/", "gen/output1.txt"),
					mustOutputPath(t, "//out/Default/", "gen/output2.txt"),
				},
			},
			wantScripts: []gotScriptCall{
				{
					Script: mustSourceFile(t, "//tools/myscript.py"),
					Args:   []string{"--foo", "bar"},
					OutputNames: []string{
						"//out/Default/gen/output1.txt",
						"//out/Default/gen/output2.txt",
					},
					Inputs: []fs.SourceFile{
						mustSourceFile(t, "//src/input1.txt"),
						mustSourceFile(t, "//src/input2.txt"),
					},
					Depfile: "my_depfile.d",
				},
			},
		},
		{
			name: "missing",
			ctx: fakeResolverContext(fakeResolverData{
				sourceFileLists: map[string][]fs.SourceFile{
					"outputs": {
						mustSourceFile(t, "//out/Default/gen/output.txt"),
					},
				},
			}),
			wantErr: &ActionMissingScriptError{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotScripts []gotScriptCall
			buildDir := mustSourceDir(t, "//out/Default/")
			tc.ctx.DeclareScript = func(script fs.SourceFile, args []string, outputNames []string, inputs []fs.SourceFile, depfile string, rspfileContent []string) ([]fs.OutputPath, error) {
				gotScripts = append(gotScripts, gotScriptCall{
					Script:         script,
					Args:           args,
					OutputNames:    outputNames,
					Inputs:         inputs,
					Depfile:        depfile,
					RspfileContent: rspfileContent,
				})
				var outputs []fs.OutputPath
				for _, name := range outputNames {
					relPath := strings.TrimPrefix(name, buildDir.Path())
					outputs = append(outputs, fs.MakeOutputPath(buildDir, relPath))
				}
				return outputs, nil
			}

			got, err := ActionSchema.Resolver(tc.ctx)

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
			if diff := cmp.Diff(tc.wantScripts, gotScripts); diff != "" {
				t.Errorf("Resolver() DeclareScript calls mismatch; diff (-want +got):\n%s", diff)
			}
		})
	}
}
