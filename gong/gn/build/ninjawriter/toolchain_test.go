// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/build/analysis"
	"go.chromium.org/build/gong/gn/build/fs"
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

func (fakeExecContext) BaseConfig() *resolve.Scope         { return &resolve.Scope{} }
func (fakeExecContext) NestedContext() resolve.ExecContext { return &fakeExecContext{} }

func TestWriteToolchain(t *testing.T) {
	tcName := "gcc"
	tcBlock := `
{
  # TODO: Using ${foo} for now until {{foo}} substitution implemented.
  tool("cc") {
    command = "gcc -c \${in} -o \${out}"
    description = "CC \${out}"
  }
  tool("alink") {
    command = "ar rcs \${out} \${in}"
    description = "AR \${out}"
  }
}`
	want := `rule alink
  command = ar rcs ${out} ${in}
  description = AR ${out}

rule cc
  command = gcc -c ${in} -o ${out}
  description = CC ${out}

`
	tokens, err := syntax.Tokenize(syntax.LiteralInput{Bytes: []byte(tcBlock)})
	if err != nil {
		t.Fatalf("failed to tokenize: %v", err)
	}
	block, err := parse.ParseExpression(tokens)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	tc, err := analysis.GenerateToolchain(
		mustDir(t, "//"),
		resolve.NewScope(&fakeExecContext{}, nil, map[string]resolve.FunctionInfo{
			"tool": analysis.ToolFunction{},
		}),
		nil,
		resolve.NewOriginlessStringValue(tcName),
		block.(*parse.BlockNode),
	)
	if err != nil {
		t.Fatalf("GenerateToolchain()=nil, %v; want nil err", err)
	}

	var buf bytes.Buffer
	err = WriteToolchain(&buf, tc)
	if err != nil {
		t.Fatalf("WriteToolchain()=%v; want nil err", err)
	}

	if diff := cmp.Diff(buf.String(), want); diff != "" {
		t.Errorf("WriteToolchain(); diff (-want +got):\n%s", diff)
	}
}
