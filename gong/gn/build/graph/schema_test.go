// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"errors"
	"testing"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

type schemaRunner struct {
	schema Schema
	dir    fs.SourceDir
}

func (schemaRunner) IsTarget() bool      { return true }
func (f schemaRunner) HelpShort() string { return "" }
func (f schemaRunner) Help() string      { return "" }
func (f schemaRunner) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value, block *parse.BlockNode) (resolve.Value, error) {
	nameValue, err := resolve.AsValue[*resolve.StringValue](args[0])
	if err != nil {
		return nil, err
	}
	_, err = f.schema.Generate(f.dir, scope, environment.Label{}, call, nameValue, block)
	if err != nil {
		return nil, err
	}
	return nil, nil
}

func TestSchema_Run(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		wantErr any
	}{
		{
			name: "normal",
			input: `example("foo") {
  sources = [ "foo.cc" ]
  deps = [ "//bar:baz", "//bar:qux" ]
}`,
		},
		{
			name: "unused",
			input: `example("foo") {
  unused = 5
}`,
			wantErr: &resolve.UnusedVarError{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokens, err := syntax.Tokenize(syntax.LiteralInput{Bytes: []byte(tc.input)})
			if err != nil {
				t.Fatalf("failed to tokenize: %v", err)
			}
			root, err := parse.Parse(tokens)
			if err != nil {
				t.Fatalf("failed to parse: %v", err)
			}

			_, err = resolve.ExecuteNode(root, resolve.NewScope(
				nil,
				map[string]resolve.FunctionInfo{
					"example": schemaRunner{
						schema: Schema{
							Name:    "example",
							Summary: "Declare an example target.",
							Vars: map[string]TargetVar{
								"name":    StringVar{},
								"sources": FileListVar{},
								"deps":    LabelListVar{&Target{}},
							},
						},
						dir: mustDir(t, "//"),
					},
				},
			))

			wantErr := tc.wantErr != nil
			gotErr := err != nil

			if gotErr != wantErr {
				t.Errorf("execute got err=%v (%T), wantErr %T", err, err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if !errors.As(err, &tc.wantErr) {
					t.Errorf("execute got err=%v (%T), wantErr %T", err, err, tc.wantErr)
				}
			}
		})
	}
}
