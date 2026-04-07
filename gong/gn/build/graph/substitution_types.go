// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

// SubstitutionPart represents a part of a substitution pattern in GN and Ninja.
type SubstitutionPart interface {
	// String is the GN representation, e.g. "{{response_file_name}}"
	String() string
	// NinjaString is the Ninja representation, e.g. "${rspfile}"
	NinjaString() string
}

// SubstitutionLiteral is a literal string inside a substitution pattern.
type SubstitutionLiteral struct {
	Literal string
}

func (s SubstitutionLiteral) String() string      { return s.Literal }
func (s SubstitutionLiteral) NinjaString() string { return s.Literal }

// SubstitutionVar represents a variable substitution in GN and Ninja.
type SubstitutionVar struct {
	GN, Ninja string
}

func (s SubstitutionVar) String() string      { return s.GN }
func (s SubstitutionVar) NinjaString() string { return s.Ninja }

var substitutionSource = SubstitutionVar{"{{source}}", "${in}"}
var substitutionOutput = SubstitutionVar{"{{output}}", "${out}"}

var substitutionSourceNamePart = SubstitutionVar{"{{source_name_part}}", ""}
var substitutionSourceFilePart = SubstitutionVar{"{{source_file_part}}", ""}
var substitutionSourceDir = SubstitutionVar{"{{source_dir}}", ""}
var substitutionSourceRootRelativeDir = SubstitutionVar{"{{source_root_relative_dir}}", ""}
var substitutionSourceGenDir = SubstitutionVar{"{{source_gen_dir}}", ""}
var substitutionSourceOutDir = SubstitutionVar{"{{source_out_dir}}", ""}

// Valid for all compiler and linker tools. These depend on the target and
// do not vary on a per-file basis.
var substitutionOutputDir = SubstitutionVar{"{{output_dir}}", "${output_dir}"}
var substitutionOutputExtension = SubstitutionVar{"{{output_extension}}", "${output_extension}"}
var substitutionTargetOutDir = SubstitutionVar{"{{target_out_dir}}", "${target_out_dir}"}
var substitutionTargetOutputName = SubstitutionVar{"{{target_output_name}}", "${target_output_name}"}

// Valid for compiler tools.
var cSubstitutionAsmFlags = SubstitutionVar{"{asmflags}}", "${asmflags}"}
var cSubstitutionCFlags = SubstitutionVar{"{{cflags}}", "${cflags}"}
var cSubstitutionCFlagsC = SubstitutionVar{"{{cflags_c}}", "${cflags_c}"}
var cSubstitutionCFlagsCc = SubstitutionVar{"{{cflags_cc}}", "${cflags_cc}"}
var cSubstitutionCFlagsObjC = SubstitutionVar{"{{cflags_objc}}", "${cflags_objc}"}
var cSubstitutionCFlagsObjCc = SubstitutionVar{"{{cflags_objcc}}", "${cflags_objcc}"}
var cSubstitutionDefines = SubstitutionVar{"{{defines}}", "${defines}"}
var cSubstitutionFrameworkDirs = SubstitutionVar{"{{framework_dirs}}", "${framework_dirs}"}
var cSubstitutionIncludeDirs = SubstitutionVar{"{{include_dirs}}", "${include_dirs}"}
var cSubstitutionModuleDeps = SubstitutionVar{"{{module_deps}}", "${module_deps}"}
var cSubstitutionModuleDepsNoSelf = SubstitutionVar{"{{module_deps_no_self}}", "${module_deps_no_self}"}

// Valid for linker tools.
var cSubstitutionLinkerInputs = SubstitutionVar{"{{inputs}}", "${in}"}
var cSubstitutionLinkerInputsNewline = SubstitutionVar{"{{inputs_newline}}", "${in_newline}"}
var cSubstitutionLdFlags = SubstitutionVar{"{{ldflags}}", "${ldflags}"}
var cSubstitutionLibs = SubstitutionVar{"{{libs}}", "${libs}"}
var cSubstitutionSoLibs = SubstitutionVar{"{{solibs}}", "${solibs}"}
var cSubstitutionRlibs = SubstitutionVar{"{{rlibs}}", "${rlibs}"}
var cSubstitutionFrameworks = SubstitutionVar{"{{frameworks}}", "${frameworks}"}
var cSubstitutionSwiftModules = SubstitutionVar{"{{swiftmodules}}", "${swiftmodules}"}

// Rust substitutions.
var substitutionCrateName = SubstitutionVar{"{{crate_name}}", "${crate_name}"}
var substitutionCrateType = SubstitutionVar{"{{crate_type}}", "${crate_type}"}
var substitutionRustExterns = SubstitutionVar{"{{externs}}", "${externs}"}
var substitutionRustDeps = SubstitutionVar{"{{rustdeps}}", "${rustdeps}"}
var substitutionRustEnv = SubstitutionVar{"{{rustenv}}", "${rustenv}"}
var substitutionRustFlags = SubstitutionVar{"{{rustflags}}", "${rustflags}"}
var substitutionRustSources = SubstitutionVar{"{{sources}}", "${sources}"}

// Used only for the args of actions.
var substitutionRspFileName = SubstitutionVar{"{{response_file_name}}", "${rspfile}"}

var generalSubstitutions = []SubstitutionPart{
	substitutionSource,
	substitutionOutput,
	substitutionOutputDir,
	substitutionOutputExtension,
	substitutionTargetOutDir,
	substitutionTargetOutputName,
	substitutionSourceNamePart,
	substitutionSourceFilePart,
	substitutionSourceDir,
	substitutionSourceRootRelativeDir,
	substitutionSourceGenDir,
	substitutionSourceOutDir,
	substitutionRspFileName,
}

var cSubstitutions = []SubstitutionPart{
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

var rustSubstitutions = []SubstitutionPart{
	substitutionCrateName,
	substitutionCrateType,
	substitutionRustExterns,
	substitutionRustDeps,
	substitutionRustEnv,
	substitutionRustFlags,
	substitutionRustSources,
}

var allSubstitutions = append(append(generalSubstitutions, cSubstitutions...), rustSubstitutions...)
