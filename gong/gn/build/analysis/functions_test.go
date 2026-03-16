// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"os"
	"testing"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

func parseGniTest(t *testing.T, file string) parse.Node {
	t.Helper()
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
	return root
}
