// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

func execToolchain(t *testing.T, input string) (*Toolchain, error) {
	t.Helper()

	tokens, err := syntax.Tokenize(mockInput{
		displayName: t.Name(),
		contents:    input,
	})
	if err != nil {
		t.Fatalf("failed to tokenize: %v", err)
	}
	root, err := parse.Parse(tokens)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	var capturedToolchain *Toolchain
	scope := resolve.NewScope(
		&scopeContext{
			settings:  NewSettings(&environment.BuildSettings{}),
			sourceDir: mustDir(t, "//"),
			itemCollector: func(i Item) {
				if tc, ok := i.(*Toolchain); ok {
					capturedToolchain = tc
				}
			},
		},
		nil,
		map[string]resolve.FunctionInfo{
			"toolchain": &toolchainFunction{},
			"tool":      &toolFunction{},
		},
	)

	_, err = resolve.ExecuteNode(root, scope)
	return capturedToolchain, err
}

func TestToolchain_Run_Valid(t *testing.T) {
	input := `
toolchain("gcc") {
  tool("cc") {
    command = "gcc {{source}} -o {{output}}"
    outputs = [ "{{output}}.o" ]
    description = "CC {{source}}"
  }
}`
	wantTools := map[string]*Tool{
		"cc": {
			name:        "cc",
			command:     "gcc {{source}} -o {{output}}",
			outputs:     []string{"{{output}}.o"},
			description: "CC {{source}}",
		},
	}

	tc, err := execToolchain(t, input)

	if err != nil {
		t.Fatalf("execToolchain()=_, %v; want nil", err)
	}
	if tc == nil {
		t.Fatal("execToolchain()=nil, _; want non-nil")
	}
	if tc.label.Name != "gcc" {
		t.Errorf("tc.label.Name = %q, want gcc", tc.label.Name)
	}
	if diff := cmp.Diff(wantTools, tc.tools, cmp.AllowUnexported(Tool{}), cmpopts.IgnoreFields(Tool{}, "definedFrom")); diff != "" {
		t.Errorf("tc.tools; diff (-want +got):\n%s", diff)
	}
}

func TestToolchain_Run_MissingName(t *testing.T) {
	input := `toolchain() {}`

	_, err := execToolchain(t, input)

	var wantErr resolve.ArgumentCountError
	if !errors.As(err, &wantErr) {
		t.Errorf("execToolchain() got err=%v (%T), wantErr %T", err, err, wantErr)
	}
}

func TestToolchain_Run_ToolMissingCommand(t *testing.T) {
	input := `
toolchain("gcc") {
  tool("cc") {
    outputs = [ "foo" ]
  }
}`
	_, err := execToolchain(t, input)
	var wantErr ToolError
	if !errors.As(err, &wantErr) {
		t.Errorf("execToolchain() got err=%v (%T), wantErr %T", err, err, wantErr)
	}
}

func TestToolchain_Run_ToolAction(t *testing.T) {
	input := `
toolchain("gcc") {
  tool("action") {
    outputs = [ "foo" ]
  }
}`
	wantTools := map[string]*Tool{
		"action": {
			name:    "action",
			outputs: []string{"foo"},
		},
	}

	tc, err := execToolchain(t, input)

	if err != nil {
		t.Fatalf("execToolchain()=_, %v; want nil", err)
	}
	if tc == nil {
		t.Fatal("execToolchain()=nil, _; want non-nil")
	}
	if diff := cmp.Diff(wantTools, tc.tools, cmp.AllowUnexported(Tool{}), cmpopts.IgnoreFields(Tool{}, "definedFrom")); diff != "" {
		t.Errorf("tc.tools; diff (-want +got):\n%s", diff)
	}
}

func TestToolchain_Run_ToolOutsideToolchain(t *testing.T) {
	input := `
tool("cc") {
  command = "gcc"
}`
	_, err := execToolchain(t, input)
	var wantErr ToolOutsideToolchain
	if !errors.As(err, &wantErr) {
		t.Errorf("execToolchain() got err=%v (%T), wantErr %T", err, err, wantErr)
	}
}
