// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

type fakeExecContext struct{}

func (fakeExecContext) BaseConfig() *resolve.Scope         { return &resolve.Scope{} }
func (fakeExecContext) NestedContext() resolve.ExecContext { return &fakeExecContext{} }

func TestToolchain_Run_Valid(t *testing.T) {
	input := `
{
  tool("cc") {
    command = "gcc {{source}} -o {{output}}"
    outputs = [ "{{output}}.o" ]
    description = "CC {{source}}"
  }
}`
	wantTools := map[string]*Tool{
		"cc": {
			Name:        "cc",
			Command:     "gcc {{source}} -o {{output}}",
			outputs:     []string{"{{output}}.o"},
			Description: "CC {{source}}",
		},
	}

	tokens, err := syntax.Tokenize(syntax.LiteralInput{Bytes: []byte(input)})
	if err != nil {
		t.Fatalf("failed to tokenize: %v", err)
	}
	block, err := parse.ParseExpression(tokens)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	tc, err := NewToolchain(
		mustDir(t, "//"),
		resolve.NewScope(&fakeExecContext{}, nil, map[string]resolve.FunctionInfo{
			"tool": ToolFunction{},
		}),
		nil,
		resolve.NewOriginlessStringValue("gcc"),
		block.(*parse.BlockNode),
	)

	if err != nil {
		t.Fatalf("execToolchain()=_, %v; want nil", err)
	}
	if tc == nil {
		t.Fatal("execToolchain()=nil, _; want non-nil")
	}
	if tc.Label().Name != "gcc" {
		t.Errorf("tc.label.Name = %q, want gcc", tc.Label().Name)
	}
	if diff := cmp.Diff(wantTools, tc.Tools, cmp.AllowUnexported(Tool{}), cmpopts.IgnoreFields(Tool{}, "definedFrom")); diff != "" {
		t.Errorf("tc.Tools; diff (-want +got):\n%s", diff)
	}
}

func TestToolchain_Run_ToolMissingCommand(t *testing.T) {
	input := `
{
  tool("cc") {
    outputs = [ "foo" ]
  }
}`
	tokens, err := syntax.Tokenize(syntax.LiteralInput{Bytes: []byte(input)})
	if err != nil {
		t.Fatalf("failed to tokenize: %v", err)
	}
	block, err := parse.ParseExpression(tokens)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	_, err = NewToolchain(
		mustDir(t, "//"),
		resolve.NewScope(&fakeExecContext{}, nil, map[string]resolve.FunctionInfo{
			"tool": ToolFunction{},
		}),
		nil,
		resolve.NewOriginlessStringValue("gcc"),
		block.(*parse.BlockNode),
	)
	var wantErr ToolError
	if !errors.As(err, &wantErr) {
		t.Errorf("execToolchain() got err=%v (%T), wantErr %T", err, err, wantErr)
	}
}

func TestToolchain_Run_ToolAction(t *testing.T) {
	input := `
{
  tool("action") {
    outputs = [ "foo" ]
  }
}`
	wantTools := map[string]*Tool{
		"action": {
			Name:    "action",
			outputs: []string{"foo"},
		},
	}

	tokens, err := syntax.Tokenize(syntax.LiteralInput{Bytes: []byte(input)})
	if err != nil {
		t.Fatalf("failed to tokenize: %v", err)
	}
	block, err := parse.ParseExpression(tokens)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	tc, err := NewToolchain(
		mustDir(t, "//"),
		resolve.NewScope(&fakeExecContext{}, nil, map[string]resolve.FunctionInfo{
			"tool": ToolFunction{},
		}),
		nil,
		resolve.NewOriginlessStringValue("gcc"),
		block.(*parse.BlockNode),
	)

	if err != nil {
		t.Fatalf("execToolchain()=_, %v; want nil", err)
	}
	if tc == nil {
		t.Fatal("execToolchain()=nil, _; want non-nil")
	}
	if diff := cmp.Diff(wantTools, tc.Tools, cmp.AllowUnexported(Tool{}), cmpopts.IgnoreFields(Tool{}, "definedFrom")); diff != "" {
		t.Errorf("tc.Tools; diff (-want +got):\n%s", diff)
	}
}

func TestToolchain_Run_ToolOutsideToolchain(t *testing.T) {
	input := `
tool("cc") {
  command = "gcc"
}`
	tokens, err := syntax.Tokenize(syntax.LiteralInput{Bytes: []byte(input)})
	if err != nil {
		t.Fatalf("failed to tokenize: %v", err)
	}
	node, err := parse.ParseExpression(tokens)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	_, err = resolve.ExecuteNode(
		node,
		resolve.NewScope(&fakeExecContext{}, nil, map[string]resolve.FunctionInfo{
			"tool": ToolFunction{},
		}),
	)
	var wantErr ToolOutsideToolchain
	if !errors.As(err, &wantErr) {
		t.Errorf("execToolchain() got err=%v (%T), wantErr %T", err, err, wantErr)
	}
}
