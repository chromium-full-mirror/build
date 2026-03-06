// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

// substitutionPart represents a part of a substitution pattern in GN and Ninja.
type substitutionPart interface {
	// String is the GN representation, e.g. "{{response_file_name}}"
	String() string
	// NinjaString is the Ninja representation, e.g. "${rspfile}"
	NinjaString() string
}

// substitutionLiteral is a literal string inside a substitution pattern.
type substitutionLiteral struct {
	Literal string
}

func (s substitutionLiteral) String() string      { return s.Literal }
func (s substitutionLiteral) NinjaString() string { return s.Literal }

// substitutionVar represents a variable substitution in GN and Ninja.
type substitutionVar struct {
	GN, Ninja string
}

func (s substitutionVar) String() string      { return s.GN }
func (s substitutionVar) NinjaString() string { return s.Ninja }

var substitutionSource = substitutionVar{"{{source}}", "${in}"}
var substitutionOutput = substitutionVar{"{{output}}", "${out}"}

// Valid for all compiler and linker tools. These depend on the target and
// do not vary on a per-file basis.
var substitutionOutputDir = substitutionVar{"{{output_dir}}", "${output_dir}"}
var substitutionOutputExtension = substitutionVar{"{{output_extension}}", "${output_extension}"}
var substitutionTargetOutDir = substitutionVar{"{{target_out_dir}}", "${target_out_dir}"}
var substitutionTargetOutputName = substitutionVar{"{{target_output_name}}", "${target_output_name}"}

// Valid for compiler tools.
var cSubstitutionAsmFlags = substitutionVar{"{asmflags}}", "${asmflags}"}
var cSubstitutionCFlags = substitutionVar{"{{cflags}}", "${cflags}"}
var cSubstitutionCFlagsC = substitutionVar{"{{cflags_c}}", "${cflags_c}"}
var cSubstitutionCFlagsCc = substitutionVar{"{{cflags_cc}}", "${cflags_cc}"}
var cSubstitutionCFlagsObjC = substitutionVar{"{{cflags_objc}}", "${cflags_objc}"}
var cSubstitutionCFlagsObjCc = substitutionVar{"{{cflags_objcc}}", "${cflags_objcc}"}
var cSubstitutionDefines = substitutionVar{"{{defines}}", "${defines}"}
var cSubstitutionFrameworkDirs = substitutionVar{"{{framework_dirs}}", "${framework_dirs}"}
var cSubstitutionIncludeDirs = substitutionVar{"{{include_dirs}}", "${include_dirs}"}
var cSubstitutionModuleDeps = substitutionVar{"{{module_deps}}", "${module_deps}"}
var cSubstitutionModuleDepsNoSelf = substitutionVar{"{{module_deps_no_self}}", "${module_deps_no_self}"}

// Valid for linker tools.
var cSubstitutionLinkerInputs = substitutionVar{"{{inputs}}", "${in}"}
var cSubstitutionLinkerInputsNewline = substitutionVar{"{{inputs_newline}}", "${in_newline}"}
var cSubstitutionLdFlags = substitutionVar{"{{ldflags}}", "${ldflags}"}
var cSubstitutionLibs = substitutionVar{"{{libs}}", "${libs}"}
var cSubstitutionSoLibs = substitutionVar{"{{solibs}}", "${solibs}"}
var cSubstitutionRlibs = substitutionVar{"{{rlibs}}", "${rlibs}"}
var cSubstitutionFrameworks = substitutionVar{"{{frameworks}}", "${frameworks}"}
var cSubstitutionSwiftModules = substitutionVar{"{{swiftmodules}}", "${swiftmodules}"}

// Rust substitutions.
var substitutionCrateName = substitutionVar{"{{crate_name}}", "${crate_name}"}
var substitutionCrateType = substitutionVar{"{{crate_type}}", "${crate_type}"}
var substitutionRustExterns = substitutionVar{"{{externs}}", "${externs}"}
var substitutionRustDeps = substitutionVar{"{{rustdeps}}", "${rustdeps}"}
var substitutionRustEnv = substitutionVar{"{{rustenv}}", "${rustenv}"}
var substitutionRustFlags = substitutionVar{"{{rustflags}}", "${rustflags}"}
var substitutionRustSources = substitutionVar{"{{sources}}", "${sources}"}

var generalSubstitutions = []substitutionPart{
	substitutionSource,
	substitutionOutput,
	substitutionOutputDir,
	substitutionOutputExtension,
	substitutionTargetOutDir,
	substitutionTargetOutputName,
}

var cSubstitutions = []substitutionPart{
	cSubstitutionAsmFlags,
	cSubstitutionCFlags,
	cSubstitutionCFlagsC,
	cSubstitutionCFlagsCc,
	cSubstitutionCFlagsObjC,
	cSubstitutionCFlagsObjCc,
	cSubstitutionDefines,
	cSubstitutionFrameworkDirs,
	cSubstitutionIncludeDirs,
	cSubstitutionModuleDeps,
	cSubstitutionModuleDepsNoSelf,
	cSubstitutionLinkerInputs,
	cSubstitutionLinkerInputsNewline,
	cSubstitutionLdFlags,
	cSubstitutionLibs,
	cSubstitutionSoLibs,
	cSubstitutionRlibs,
	cSubstitutionFrameworks,
	cSubstitutionSwiftModules,
}

var rustSubstitutions = []substitutionPart{
	substitutionCrateName,
	substitutionCrateType,
	substitutionRustExterns,
	substitutionRustDeps,
	substitutionRustEnv,
	substitutionRustFlags,
	substitutionRustSources,
}

var allSubstitutions = append(append(generalSubstitutions, cSubstitutions...), rustSubstitutions...)
