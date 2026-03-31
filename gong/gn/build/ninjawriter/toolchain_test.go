// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

func mustDir(t *testing.T, path string) fs.SourceDir {
	t.Helper()
	d, err := fs.MakeSourceDir(path)
	if err != nil {
		t.Fatalf("failed to make source dir %q: %v", path, err)
	}
	return d
}

type fakeExecContext struct{}

func (fakeExecContext) BaseConfig() *resolve.Scope                         { return &resolve.Scope{} }
func (fakeExecContext) NestedContext() resolve.ExecContext                 { return &fakeExecContext{} }
func (fakeExecContext) ProgrammaticBuiltin(_ string) (resolve.Value, bool) { return nil, false }

func TestWriteToolchain(t *testing.T) {
	// Referencing "clang++" catches accidental use of html/template instead of text/template,
	// which would escape it and result in "clang&#43;&#43;" being written instead.
	tcName := "clang"
	tcBlock := `
{
  tool("cc") {
    command = "clang -c {{source}} -o {{output}}"
    description = "CC {{output}}"
  }
  tool("link") {
    command = "clang++ {{ldflags}} -o {{output}} @{{output}}.rsp"
    description = "LINK {{output}}"
    rspfile = "{{output}}.rsp"
    rspfile_content = "{{inputs}} {{solibs}} {{libs}}"
  }
}`
	want := `rule cc
  command = clang -c ${in} -o ${out}
  description = CC ${out}

rule link
  command = clang++ ${ldflags} -o ${out} @${out}.rsp
  description = LINK ${out}
  rspfile = ${out}.rsp
  rspfile_content = ${in} ${solibs} ${libs}

build phony/default: phony obj/hello_world/src/hello_world
subninja obj/hello_world/src/hello_world.ninja
`
	tokens, err := syntax.Tokenize(syntax.LiteralInput{Bytes: []byte(tcBlock)})
	if err != nil {
		t.Fatalf("failed to tokenize: %v", err)
	}
	block, err := parse.ParseExpression(tokens)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	tc, err := graph.NewToolchain(
		mustDir(t, "//"),
		resolve.NewScope(&fakeExecContext{}, map[string]resolve.FunctionInfo{
			"tool": graph.ToolFunction{},
		}),
		nil,
		resolve.NewOriginlessStringValue(tcName),
		block.(*parse.BlockNode),
	)
	if err != nil {
		t.Fatalf("GenerateToolchain()=nil, %v; want nil err", err)
	}

	var buf bytes.Buffer
	err = writeToolchain(&buf, tc, []string{
		"build phony/default: phony obj/hello_world/src/hello_world",
		"subninja obj/hello_world/src/hello_world.ninja",
	})
	if err != nil {
		t.Fatalf("WriteToolchain()=%v; want nil err", err)
	}

	if diff := cmp.Diff(buf.String(), want); diff != "" {
		t.Errorf("WriteToolchain(); diff (-want +got):\n%s", diff)
	}
}
