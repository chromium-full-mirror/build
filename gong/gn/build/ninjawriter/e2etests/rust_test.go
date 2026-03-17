// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"testing"
)

func TestRust_Executable(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
toolchain("tc") {
  tool("rust_bin") { command = "rustc" }
}

executable("app") {
  crate_root = "main.rs"
}`,
			"main.rs": "",
		},
		map[string]string{
			"build.ninja": `
# TODO: ninja_required_version
# TODO: rule gn
# TODO: rule build.ninja.stamp
# TODO: rule build.ninja
subninja toolchain.ninja
build app: phony obj/app
build $:app: phony obj/app

build all: phony $
    obj/app

default all
`,
			"toolchain.ninja": `
rule rust_bin
  command = rustc

subninja obj/app.ninja
`,
			"obj/app.ninja": `
output_dir = obj
target_output_name = app
target_out_dir = obj

build obj/app: rust_bin ../../main.rs
  crate_name = app
  crate_type = bin
  externs =
  rustdeps =
  rustflags =
`,
		},
	)
}
