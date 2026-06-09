// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"testing"
)

func TestGroup_Simple(t *testing.T) {
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
build foo: phony
build $:foo: phony

build all: phony

default all
`,
			"toolchain.ninja": `
rule stamp
  command = touch

build phony/foo: phony ` + `
`,
		},
	)
}

func TestGroup_DepPropagation(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
toolchain("tc") {
  tool("stamp") { command = "touch" }
}

static_library("foo") {
  sources = [ "foo.cc" ]
}

static_library("bar") {
  sources = [ "bar.cc" ]
}

group("my_group") {
  deps = [ ":foo", ":bar" ]
}

executable("app") {
  sources = [ "main.cc" ]
  deps = [ ":my_group" ]
}
`,
		},
		map[string]string{
			"build.ninja": `
# TODO: ninja_required_version
# TODO: rule gn
# TODO: rule build.ninja.stamp
# TODO: rule build.ninja
subninja toolchain.ninja
build foo: phony obj/libfoo.a
build bar: phony obj/libbar.a
build my_group: phony obj/libfoo.a obj/libbar.a
build app: phony obj/app
build $:foo: phony obj/libfoo.a
build $:bar: phony obj/libbar.a
build $:my_group: phony obj/libfoo.a obj/libbar.a
build $:app: phony obj/app

build all: phony $
    obj/libfoo.a $
    obj/libbar.a $
    phony/my_group $
    obj/app

default all
`,
			"toolchain.ninja": `
rule stamp
  command = touch

subninja obj/foo.ninja
subninja obj/bar.ninja
build phony/my_group: phony obj/libfoo.a obj/libbar.a
subninja obj/app.ninja
`,
			"obj/app.ninja": `
output_dir = obj
target_output_name = app
target_out_dir = obj

build obj/app.main.cc.o: cxx ../../main.cc
  source_file_part =
  source_name_part =
build obj/app: link obj/app.main.cc.o obj/libfoo.a obj/libbar.a
  frameworks =
  ldflags =
  libs =
  swiftmodules =
`,
		},
	)
}
