// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"testing"
)

// TODO: need to fix obj/ showing up in the directory paths.

func TestAction_Basic(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
toolchain("tc") {
  tool("stamp") { command = "touch" }
}

action("foo") {
  script = "foo.py"
  outputs = [ "gen/foo.out" ]  # TODO: should use $target_gen_dir once implemented
  args = [ "--cool", "arg" ]
}`,
		},
		map[string]string{
			"build.ninja": `
# TODO: ninja_required_version
# TODO: rule gn
# TODO: rule build.ninja.stamp
# TODO: rule build.ninja
subninja toolchain.ninja
build foo: phony obj/gen/foo.out
build $:foo: phony obj/gen/foo.out

build all: phony $
    phony/foo

default all
`,
			"toolchain.ninja": `
rule stamp
  command = touch

rule ___foo____tc__rule
  command = /path/to/my/python ../../foo.py --cool arg
  description = ACTION //:foo(//:tc)
  restat = 1

build obj/gen/foo.out: ___foo____tc__rule | ../../foo.py
build phony/foo: phony obj/gen/foo.out
`,
		},
	)
}

func TestAction_MultipleOutputs(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
toolchain("tc") {
  tool("stamp") { command = "touch {{output}}" }
}

action("multi_output") {
  script = "tools/myscript.py"
  outputs = [
    "gen/output1.txt",  # TODO: should use $target_gen_dir once implemented
    "gen/output2.txt",  # TODO: should use $target_gen_dir once implemented
  ]
}`,
			"tools/myscript.py": "",
		},
		map[string]string{
			"build.ninja": `
# TODO: ninja_required_version
# TODO: rule gn
# TODO: rule build.ninja.stamp
# TODO: rule build.ninja
subninja toolchain.ninja
build multi_output: phony obj/gen/output1.txt obj/gen/output2.txt
build $:multi_output: phony obj/gen/output1.txt obj/gen/output2.txt

build all: phony $
    phony/multi_output

default all
`,
			"toolchain.ninja": `
rule stamp
  command = touch ${out}

rule ___multi_output____tc__rule
  command = /path/to/my/python ../../tools/myscript.py
  description = ACTION //:multi_output(//:tc)
  restat = 1

build obj/gen/output1.txt obj/gen/output2.txt: ___multi_output____tc__rule | ../../tools/myscript.py
build phony/multi_output: phony obj/gen/output1.txt obj/gen/output2.txt
`,
		},
	)
}

func TestAction_RspFile(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
toolchain("tc") {
  tool("stamp") { command = "touch" }
}

action("foo") {
  script = "foo.py"
  outputs = [ "gen/foo.out" ]
  response_file_contents = [ "--rsp", "file", "contents" ]
  args = [ "--rsp-file", "{{response_file_name}}" ]
}`,
		},
		map[string]string{
			"build.ninja": `
# TODO: ninja_required_version
# TODO: rule gn
# TODO: rule build.ninja.stamp
# TODO: rule build.ninja
subninja toolchain.ninja
build foo: phony obj/gen/foo.out
build $:foo: phony obj/gen/foo.out

build all: phony $
    phony/foo

default all
`,
			"toolchain.ninja": `
rule stamp
  command = touch

rule ___foo____tc__rule
  command = /path/to/my/python ../../foo.py --rsp-file ${rspfile}
  description = ACTION //:foo(//:tc)
  restat = 1
  rspfile = ___foo____tc__rule.rsp
  rspfile_content = --rsp file contents

build obj/gen/foo.out: ___foo____tc__rule | ../../foo.py
build phony/foo: phony obj/gen/foo.out
`,
		},
	)
}
