// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package a

import "testing"

type testCase struct {
	name string
	desc string
}

func TestSubtestNames(t *testing.T) {
	// Good cases
	t.Run("good_name", func(t *testing.T) {})
	t.Run("GoodName", func(t *testing.T) {})

	// Bad inline subtest names with space or slash
	t.Run("bad name with space", func(t *testing.T) {}) // want `subtest name "bad name with space" contains spaces or slashes`
	t.Run("bad/slash", func(t *testing.T) {})           // want `subtest name "bad/slash" contains spaces or slashes`

	// Using tc.desc in t.Run
	tc := testCase{
		name: "bad name in table", // want `table case .name. "bad name in table" contains spaces or slashes`
		desc: "prose description",
	}

	t.Run(tc.desc, func(t *testing.T) {}) // want `uses .desc field instead of .name in t.Run`
	t.Run(tc.name, func(t *testing.T) {})
}
