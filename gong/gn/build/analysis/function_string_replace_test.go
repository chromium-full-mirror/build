// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"os"
	"testing"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

func TestStringReplace(t *testing.T) {
	file := "testdata/string_replace.gni"
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("failed to read %q: %v", file, err)
	}
	tokens, err := syntax.Tokenize(syntax.LiteralInput{
		CustomName: file,
		Bytes:      content,
	})
	if err != nil {
		t.Fatalf("failed to tokenize %q: %v", file, err)
	}
	root, err := parse.Parse(tokens)
	if err != nil {
		t.Fatalf("failed to parse %q: %v", file, err)
	}

	_, err = resolve.ExecuteNode(root, resolve.NewScope(nil, nil, map[string]resolve.FunctionInfo{
		"assert":         resolve.AssertFunction{},
		"assert_failure": resolve.AssertFailureFunction{},
		"string_replace": stringReplaceFunction{},
	}))
	if err != nil {
		t.Errorf("execute failed: %v", err)
	}
}
