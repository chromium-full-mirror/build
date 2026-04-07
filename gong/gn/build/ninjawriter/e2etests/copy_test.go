// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"testing"
)

// TODO: add a TestCopy_Basic that uses $target_out_dir, which is the more typical way to
// use it. But we haven't implemented support for $target_out_dir yet, so it would fail.

// TODO: this is obviously incorrect. substitution parsing needs to be implemented.
func TestCopy_Substitution(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
toolchain("tc") {
  tool("copy") { command = "cp {{source}} {{output}}" }
  tool("stamp") { command = "touch" }
}

copy("my_copy") {
  sources = [ "src/foo.txt", "src/bar.txt" ]
  outputs = [ "{{source_out_dir}}/{{source_name_part}}.out" ]
}`,
		},
		map[string]string{
			"build.ninja": `
# TODO: ninja_required_version
# TODO: rule gn
# TODO: rule build.ninja.stamp
# TODO: rule build.ninja
subninja toolchain.ninja
build my_copy: phony obj/{{source_out_dir}}/{{source_name_part}}.out obj/{{source_out_dir}}/{{source_name_part}}.out
build $:my_copy: phony obj/{{source_out_dir}}/{{source_name_part}}.out obj/{{source_out_dir}}/{{source_name_part}}.out

build all: phony $
    obj/{{source_out_dir}}/{{source_name_part}}.out

default all
`,
			"toolchain.ninja": `
rule copy
  command = cp ${in} ${out}

rule stamp
  command = touch

build obj/{{source_out_dir}}/{{source_name_part}}.out: copy ../../src/foo.txt
build obj/{{source_out_dir}}/{{source_name_part}}.out: copy ../../src/bar.txt
build phony/my_copy: phony obj/{{source_out_dir}}/{{source_name_part}}.out obj/{{source_out_dir}}/{{source_name_part}}.out
`,
		},
	)
}
