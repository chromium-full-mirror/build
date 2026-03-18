// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"testing"

	"go.chromium.org/build/gong/gn/resolve"
)

func TestFilter(t *testing.T) {
	root := parseGniTest(t, "testdata/filter.gni")

	_, err := resolve.ExecuteNode(root, resolve.NewScope(nil, nil, map[string]resolve.FunctionInfo{
		"assert":         resolve.AssertFunction{},
		"assert_failure": resolve.AssertFailureFunction{},
		"filter_include": filterIncludeFunction{},
		"filter_exclude": filterExcludeFunction{},
	}))
	if err != nil {
		t.Errorf("execute failed: %v", err)
	}
}
