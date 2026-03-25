// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"testing"

	"go.chromium.org/build/gong/gn/resolve"
)

func TestTemplate(t *testing.T) {
	root := parseGniTest(t, "testdata/template.gni")

	_, err := resolve.ExecuteNode(root, resolve.NewScope(nil, map[string]resolve.FunctionInfo{
		"assert":         resolve.AssertFunction{},
		"assert_failure": resolve.AssertFailureFunction{},
		"template":       templateFunction{},
	}))
	if err != nil {
		t.Errorf("execute failed: %v", err)
	}
}
