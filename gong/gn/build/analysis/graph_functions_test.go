// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"errors"
	"testing"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

func execToolchain(t *testing.T, input string) (*graph.Toolchain, error) {
	t.Helper()

	tokens, err := syntax.Tokenize(syntax.LiteralInput{
		CustomName: t.Name(),
		Bytes:      []byte(input),
	})
	if err != nil {
		t.Fatalf("failed to tokenize: %v", err)
	}
	root, err := parse.Parse(tokens)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	var capturedToolchain *graph.Toolchain
	scope := resolve.NewScope(
		&scopeContext{
			settings:  NewSettings(&environment.BuildSettings{}, NewImportManager(&fs.InputFileManager{})),
			sourceDir: mustDir(t, "//"),
			itemCollector: func(i graph.Item) {
				if tc, ok := i.(*graph.Toolchain); ok {
					capturedToolchain = tc
				}
			},
		},
		map[string]resolve.FunctionInfo{
			"toolchain": &toolchainFunction{},
		},
	)

	_, err = resolve.ExecuteNode(root, scope)
	return capturedToolchain, err
}

func TestToolchain_Run_MissingName(t *testing.T) {
	input := `toolchain() {}`

	_, err := execToolchain(t, input)

	var wantErr resolve.ArgumentCountError
	if !errors.As(err, &wantErr) {
		t.Errorf("execToolchain() got err=%v (%T), wantErr %T", err, err, wantErr)
	}
}
