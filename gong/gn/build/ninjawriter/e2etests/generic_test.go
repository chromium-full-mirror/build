// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"testing"
)

func TestGeneric_Group(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
toolchain("tc") {
  tool("stamp") { command = "touch" }
}

group("foo") {
}`,
		},
		map[string]string{
			"build.ninja": `
# TODO: ninja_required_version
# TODO: rule gn
# TODO: rule build.ninja.stamp
# TODO: rule build.ninja
subninja toolchain.ninja
# TODO: build all
`,
			"toolchain.ninja": `
rule stamp
  command = touch

build phony/foo: phony ` + `
`,
		},
	)
}
