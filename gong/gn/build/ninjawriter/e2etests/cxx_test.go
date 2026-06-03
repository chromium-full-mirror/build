// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"testing"
)

func TestCxx_Executable(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
toolchain("tc") {
  tool("cxx") { command = "clang++ -c {{source}} -o {{output}}" }
  tool("link") { command = "ld" }
}

executable("app") {
  sources = [ "main.cc" ]
  cflags = [ "-O2" ]
  defines = [ "BUFFER_SIZE=(1<<16)" ]
}`,
			"main.cc": "",
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
rule cxx
  command = clang++ -c ${in} -o ${out}

rule link
  command = ld

subninja obj/app.ninja
`,
			"obj/app.ninja": `
output_dir = obj
target_output_name = app
target_out_dir = obj
cflags = -O2
defines = -DBUFFER_SIZE=\(1\<\<16\)

build obj/app.main.cc.o: cxx ../../main.cc
  source_file_part =
  source_name_part =
build obj/app: link obj/app.main.cc.o
  frameworks =
  ldflags =
  libs =
  swiftmodules =
`,
		},
	)
}

func TestCxx_SharedLibrary(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
toolchain("tc") {
  tool("cxx") { command = "clang++" }
  tool("solink") { command = "ld -shared" }
}

shared_library("foo") {
  sources = [ "lib.cc" ]
}`,
			"lib.cc": "",
		},
		map[string]string{
			"build.ninja": `
# TODO: ninja_required_version
# TODO: rule gn
# TODO: rule build.ninja.stamp
# TODO: rule build.ninja
subninja toolchain.ninja
build foo: phony obj/libfoo.so
build $:foo: phony obj/libfoo.so

build all: phony $
    obj/libfoo.so

default all
`,
			"toolchain.ninja": `
rule cxx
  command = clang++

rule solink
  command = ld -shared

subninja obj/foo.ninja
`,
			"obj/foo.ninja": `
output_extension = .so
output_dir = obj
target_output_name = libfoo
target_out_dir = obj

build obj/libfoo.lib.cc.o: cxx ../../lib.cc
  source_file_part =
  source_name_part =
build obj/libfoo.so: solink obj/libfoo.lib.cc.o
  frameworks =
  ldflags =
  libs =
  swiftmodules =
`,
		},
	)
}

func TestCxx_StaticLibrary(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
toolchain("tc") {
  tool("cxx") { command = "clang++" }
  tool("alink") { command = "ar" }
}

static_library("foo") {
  sources = [ "lib.cc" ]
}`,
			"lib.cc": "",
		},
		map[string]string{
			"build.ninja": `
# TODO: ninja_required_version
# TODO: rule gn
# TODO: rule build.ninja.stamp
# TODO: rule build.ninja
subninja toolchain.ninja
build foo: phony obj/libfoo.a
build $:foo: phony obj/libfoo.a

build all: phony $
    obj/libfoo.a

default all
`,
			"toolchain.ninja": `
rule alink
  command = ar

rule cxx
  command = clang++

subninja obj/foo.ninja
`,
			"obj/foo.ninja": `
output_extension = .a
output_dir = obj
target_output_name = libfoo
target_out_dir = obj

build obj/libfoo.lib.cc.o: cxx ../../lib.cc
  source_file_part =
  source_name_part =
build obj/libfoo.a: alink obj/libfoo.lib.cc.o
  arflags =
`,
		},
	)
}

func TestCxx_StaticLibrary_PrefixOverride(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
toolchain("tc") {
  tool("cxx") { command = "clang++" }
  tool("alink") { command = "ar" }
}

static_library("foo") {
  sources = [ "lib.cc" ]
  output_prefix_override = true
}`,
			"lib.cc": "",
		},
		map[string]string{
			"build.ninja": `
# TODO: ninja_required_version
# TODO: rule gn
# TODO: rule build.ninja.stamp
# TODO: rule build.ninja
subninja toolchain.ninja
build foo: phony obj/foo.a
build $:foo: phony obj/foo.a

build all: phony $
    obj/foo.a

default all
`,
			"toolchain.ninja": `
rule alink
  command = ar

rule cxx
  command = clang++

subninja obj/foo.ninja
`,
			"obj/foo.ninja": `
output_extension = .a
output_dir = obj
target_output_name = foo
target_out_dir = obj

build obj/foo.lib.cc.o: cxx ../../lib.cc
  source_file_part =
  source_name_part =
build obj/foo.a: alink obj/foo.lib.cc.o
  arflags =
`,
		},
	)
}

func TestCxx_LibPropagation(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
toolchain("tc") {
  tool("cxx") { command = "clang++" }
  tool("solink") { command = "ld -shared" }
  tool("link") { command = "ld" }
  tool("alink") { command = "ar" }
}

executable("app") {
  sources = [ "main.cc" ]
  deps = [ ":foo", ":bar" ]
}

shared_library("foo") {
  sources = [ "libfoo.cc" ]
  deps = [ ":baz" ]
  libs = [ "protobuf" ]
}

static_library("bar") {
  sources = [ "libbar.cc" ]
  deps = [ ":baz" ]
  libs = [ "protoc" ]
}

static_library("baz") {
  sources = [ "libbaz.cc" ]
  libs = [ "log" ]
}`,
		},
		map[string]string{
			// executable :app
			// -lprotoc comes from the dep on static_library :bar
			// -llog comes from the transitive dep :bar -> static_library :baz
			// Note how -lprotobuf from shared_library :foo is not propagated.
			"obj/app.ninja": `
output_dir = obj
target_output_name = app
target_out_dir = obj

build obj/app.main.cc.o: cxx ../../main.cc
  source_file_part =
  source_name_part =
build obj/app: link obj/app.main.cc.o obj/libfoo.so obj/libbar.a` +
				// TODO: fix obj/libbaz.a not being listed here?
				`
  frameworks =
  ldflags =
  libs = -lprotoc -llog
  swiftmodules =
`,
			// shared_library :foo
			// -lprotobuf comes from the target definition
			// -llog comes from the dep on static_library :baz
			"obj/foo.ninja": `
output_extension = .so
output_dir = obj
target_output_name = libfoo
target_out_dir = obj

build obj/libfoo.libfoo.cc.o: cxx ../../libfoo.cc
  source_file_part =
  source_name_part =
build obj/libfoo.so: solink obj/libfoo.libfoo.cc.o
  frameworks =
  ldflags =
  libs = -lprotobuf -llog
  swiftmodules =
`,
			// static_library :bar
			// -l flags should not show up.
			"obj/bar.ninja": `
output_extension = .a
output_dir = obj
target_output_name = libbar
target_out_dir = obj

build obj/libbar.libbar.cc.o: cxx ../../libbar.cc
  source_file_part =
  source_name_part =
build obj/libbar.a: alink obj/libbar.libbar.cc.o
  arflags =
`,
			// static_library :baz
			// -l flags should not show up.
			"obj/baz.ninja": `
output_extension = .a
output_dir = obj
target_output_name = libbaz
target_out_dir = obj

build obj/libbaz.libbaz.cc.o: cxx ../../libbaz.cc
  source_file_part =
  source_name_part =
build obj/libbaz.a: alink obj/libbaz.libbaz.cc.o
  arflags =
`,
		},
	)
}

func TestCxx_FrameworkPropagation(t *testing.T) {
	runTest(t,
		map[string]string{
			"build/BUILDCONFIG.gn": `
set_default_toolchain("//:tc")`,
			"BUILD.gn": `
toolchain("tc") {
  tool("cxx") { command = "clang++" }
  tool("solink") { command = "ld -shared" }
  tool("link") { command = "ld" }
  tool("alink") { command = "ar" }
}

executable("app") {
  sources = [ "main.cc" ]
  deps = [ ":foo", ":bar" ]
}

shared_library("foo") {
  sources = [ "libfoo.cc" ]
  deps = [ ":baz" ]
  frameworks = [ "SystemConfiguration.framework" ]
}

static_library("bar") {
  sources = [ "libbar.cc" ]
  deps = [ ":baz" ]
  frameworks = [ "Security.framework" ]
}

static_library("baz") {
  sources = [ "libbaz.cc" ]
  frameworks = [ "Foundation.framework" ]
}`,
			"main.cc":   "",
			"libfoo.cc": "",
			"libbar.cc": "",
			"libbaz.cc": "",
		},
		map[string]string{
			"obj/app.ninja": `
output_dir = obj
target_output_name = app
target_out_dir = obj

build obj/app.main.cc.o: cxx ../../main.cc
  source_file_part =
  source_name_part =
build obj/app: link obj/app.main.cc.o obj/libfoo.so obj/libbar.a
  frameworks = -framework Security -framework Foundation
  ldflags =
  libs =
  swiftmodules =
`,
			"obj/foo.ninja": `
output_extension = .so
output_dir = obj
target_output_name = libfoo
target_out_dir = obj

build obj/libfoo.libfoo.cc.o: cxx ../../libfoo.cc
  source_file_part =
  source_name_part =
build obj/libfoo.so: solink obj/libfoo.libfoo.cc.o
  frameworks = -framework SystemConfiguration -framework Foundation
  ldflags =
  libs =
  swiftmodules =
`,
			"obj/bar.ninja": `
output_extension = .a
output_dir = obj
target_output_name = libbar
target_out_dir = obj

build obj/libbar.libbar.cc.o: cxx ../../libbar.cc
  source_file_part =
  source_name_part =
build obj/libbar.a: alink obj/libbar.libbar.cc.o
  arflags =
`,
			"obj/baz.ninja": `
output_extension = .a
output_dir = obj
target_output_name = libbaz
target_out_dir = obj

build obj/libbaz.libbaz.cc.o: cxx ../../libbaz.cc
  source_file_part =
  source_name_part =
build obj/libbaz.a: alink obj/libbaz.libbaz.cc.o
  arflags =
`,
		},
	)
}
