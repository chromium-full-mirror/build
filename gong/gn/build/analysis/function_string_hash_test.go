// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"testing"

	"go.chromium.org/build/gong/gn/resolve"
)

func TestStringHash(t *testing.T) {
	root := parseGniTest(t, "testdata/string_hash.gni")

	_, err := resolve.ExecuteNode(root, resolve.NewScope(nil, map[string]resolve.FunctionInfo{
		"assert":         resolve.AssertFunction{},
		"assert_failure": resolve.AssertFailureFunction{},
		"string_hash":    stringHashFunction{},
	}))
	if err != nil {
		t.Errorf("execute failed: %v", err)
	}
}
