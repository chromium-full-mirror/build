// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"testing"
)

func TestTemplate_Group(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
template("my_group") {
  helper_name = "${target_name}_helper"
  group(helper_name) {
  }
  group(target_name) {
    deps = [ ":${helper_name}" ]
  }
}

my_group("foo") {
}

toolchain("tc") {
  tool("stamp") { command = "touch" }
}`,
		},
		map[string]string{
			"build.ninja": `
# TODO: ninja_required_version
# TODO: rule gn
# TODO: rule build.ninja.stamp
# TODO: rule build.ninja
subninja toolchain.ninja
build foo_helper: phony
build foo: phony
build $:foo_helper: phony
build $:foo: phony

build all: phony

default all
`,
			"toolchain.ninja": `
rule stamp
  command = touch

build phony/foo_helper: phony ` +
				// TODO: fix missing dep on foo_helper?
				`
build phony/foo: phony ` + `
`,
		},
	)
}
