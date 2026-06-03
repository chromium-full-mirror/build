// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package schemas defines target implementations.
// The word "schema" is an implementation detail specific to gong.
package schemas

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

// DefaultMetadata is a minimal implementation of resolution metadata
// that only provides outputs of the target.
type DefaultMetadata struct {
	OutputPaths []fs.OutputPath
}

// Outputs returns the output(s) from this target.
func (m DefaultMetadata) Outputs() []fs.OutputPath {
	return m.OutputPaths
}

// CxxInfo provides information regarding compilation and linking of C++.
type CxxInfo struct {
	DefaultMetadata
	// LibFlags specifies libraries to link against (without -l).
	LibFlags []string
	// LibraryFiles specifies compiled libraries for linking.
	LibraryFiles []fs.OutputPath
	// Frameworks specifies frameworks to link against.
	Frameworks []string
}

type sourceFileType int

const (
	sourceUnknown sourceFileType = iota
	sourceAsm
	sourceC
	sourceCpp
	sourceH
	sourceM
	sourceMm
	sourceModuleMap
	sourceS
	sourceRc
	sourceO
	sourceDef
	sourceRs
	sourceGo
	sourceSwift
)

// sourceFileCategory represents GN's relationship between source inputs and output binaries.
// It is not allowed to mix source inputs that fall into more than one binary category.
type sourceFileCategory int

const (
	binaryUncategorized sourceFileCategory = iota
	binaryC
	binaryRust
	binaryGo
	binarySwift
)

// Naive port of SourceFile::GetSourceFileType that can very likely be optimized.
// That's not really a priority to look into right now, though.
// https://source.chromium.org/gn/gn/+/main:src/gn/source_file.cc;l=32;drc=487f8353f15456474437df32bb186187b0940b45
//
// In addition, the category of the file type is also returned.
// https://source.chromium.org/gn/gn/+/main:src/gn/source_file.cc;l=198-217;drc=487f8353f15456474437df32bb186187b0940b45
//
// NOTE: .swiftmodule is currently not ported until necessary.
// It is not allowed to use .swiftmodule as a source input, so don't bother categorizing it
// until we start implementing real support for Swift in the schemas.
// Instead let those files fall through to sourceUnknown so that we can error as expected.
func fileTypeCategory(file string) (sourceFileType, sourceFileCategory) {
	switch path.Ext(file) {
	case ".c":
		return sourceC, binaryC
	case ".h":
		return sourceH, binaryC
	case ".m":
		return sourceM, binaryC
	case ".o", ".obj":
		return sourceO, binaryC
	case ".S", ".s", ".asm":
		return sourceS, binaryC
	case ".cc", ".cxx", ".cpp", ".c++":
		return sourceCpp, binaryC
	case ".go":
		return sourceGo, binaryGo
	case ".hh", ".hpp", ".hpp11", ".hxx", ".inc", ".ipp", ".inl":
		return sourceH, binaryC
	case ".mm":
		return sourceMm, binaryC
	case ".rc":
		return sourceRc, binaryC
	case ".rs":
		return sourceRs, binaryRust
	case ".def":
		return sourceDef, binaryC
	case ".swift":
		// .swiftmodule is deliberately not ported yet. See doc comment.
		return sourceSwift, binarySwift
	case ".modulemap":
		return sourceModuleMap, binaryC
	default:
		return sourceUnknown, binaryUncategorized
	}
}

// formatDefines returns the defines with -D prefixed, without escaping or joining.
func formatDefines(defines []string) []string {
	if len(defines) == 0 {
		return nil
	}
	out := make([]string, 0, len(defines))
	for _, d := range defines {
		out = append(out, "-D"+d)
	}
	return out
}

// formatLibExpansions returns the libraries with -l prefixed, without escaping or joining.
func formatLibExpansions(libs []string) []string {
	if len(libs) == 0 {
		return nil
	}
	out := make([]string, 0, len(libs))
	for _, l := range libs {
		out = append(out, "-l"+l)
	}
	return out
}

// formatFrameworkExpansions returns the frameworks formatted into Apple linker flags, without escaping or joining.
func formatFrameworkExpansions(frameworks []string) []string {
	if len(frameworks) == 0 {
		return nil
	}
	out := make([]string, 0, len(frameworks)*2)
	for _, f := range frameworks {
		name, _ := strings.CutSuffix(f, ".framework")
		out = append(out, "-framework", name)
	}
	return out
}

// collectLibs returns all libraries to link against, including those from dependencies.
func collectLibs(ctx graph.ResolverContext) ([]string, error) {
	libs := slices.Clone(ctx.ConfigValues.Libs)
	for dep, err := range ctx.ResolvedTargetsFor("deps") {
		if err != nil {
			return nil, fmt.Errorf("failed to collect deps: %w", err)
		}
		if ccInfo, ok := dep.Metadata.(CxxInfo); ok {
			libs = append(libs, ccInfo.LibFlags...)
		}
	}
	return libs, nil
}

// collectFrameworks returns all frameworks to link against, including those from dependencies.
func collectFrameworks(ctx graph.ResolverContext) ([]string, error) {
	frameworks := slices.Clone(ctx.ConfigValues.Frameworks)
	for dep, err := range ctx.ResolvedTargetsFor("deps") {
		if err != nil {
			return nil, fmt.Errorf("failed to collect deps: %w", err)
		}
		if ccInfo, ok := dep.Metadata.(CxxInfo); ok {
			frameworks = append(frameworks, ccInfo.Frameworks...)
		}
	}
	return frameworks, nil
}

var (
	ExecutableSchema = graph.Schema{
		Name:    "executable",
		Summary: "Declare an executable target.",
		Vars: map[string]graph.TargetVar{
			// TODO: support more variables.
			"sources":      graph.FileListVar{},
			"crate_name":   graph.StringVar{},
			"crate_root":   graph.FileVar{},
			"deps":         graph.LabelListVar{Expected: &graph.Target{}},
			"aliased_deps": graph.ScopeOfLabelsVar{Invert: true},
			"outputs":      graph.FileListVar{},
		},
		Resolver: func(ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
			name, err := ctx.StringFor("name")
			if err != nil {
				return nil, err
			}
			targetCategory := binaryUncategorized
			var inputs []fs.SourceFile
			for source, err := range ctx.SourceFilesFor("sources") {
				if err != nil {
					// TODO: ctx.SourceFilesFor might need to be (iter.Seq2[fs.SourceFile, error], error)
					// or just return nil iterator if "sources" doesn't exist.
					// Otherwise, can't easily distinguish between failure iterating next sourcefile and
					// error because "sources" doesn't exist.
					break
				}
				sourceName := source.Filename()
				sourceType, category := fileTypeCategory(source.Filename())
				if sourceType == sourceUnknown {
					return nil, BinaryInvalidSourceError{
						targetName: "executable",
						sourceName: sourceName,
					}
				}
				if targetCategory == binaryUncategorized {
					targetCategory = category
				} else if targetCategory != category {
					return nil, BinaryMixedSourcesError{}
				}
				inputs = append(inputs, source)
			}
			switch targetCategory {
			case binaryC:
				return cExecutableResolver(name, inputs, ctx)
			case binaryRust:
				return rustBinaryResolver(name, false, inputs, ctx)
			case binaryUncategorized:
				if _, err := ctx.SourceFileFor("crate_root"); err == nil {
					// For Rust targets, if the only source file is the root `sources` can be
					// omitted/empty.
					return rustBinaryResolver(name, false, inputs, ctx)
				}
				// Targets without sources are otherwise treated as C/C++.
				return cExecutableResolver(name, inputs, ctx)
			}
			return nil, NotImplementedError{
				what: "support for executables other than c and rust",
			}
		},
	}
	SharedLibrarySchema = graph.Schema{
		Name:    "shared_library",
		Summary: "Declare a shared library target.",
		Vars: map[string]graph.TargetVar{
			// TODO: support more variables.
			"sources": graph.FileListVar{},
			"deps":    graph.LabelListVar{Expected: &graph.Target{}},
		},
		Resolver: func(ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
			name, err := ctx.StringFor("name")
			if err != nil {
				return nil, err
			}
			targetCategory := binaryUncategorized
			var linkInputs []fs.SourceFile
			outName := fmt.Sprintf("lib%s", name)
			override, err := ctx.BoolFor("output_prefix_override")
			if err == nil && override {
				outName = name
			}
			sharedExpansions := &graph.SimpleExpansions{Elems: map[string][]string{
				"cflags":  ctx.ConfigValues.Cflags,
				"defines": formatDefines(ctx.ConfigValues.Defines),
			}}
			for source := range ctx.SourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceType, category := fileTypeCategory(source.Filename())
				if targetCategory == binaryUncategorized {
					targetCategory = category
				} else if targetCategory != category {
					return nil, BinaryMixedSourcesError{}
				}
				if sourceType == sourceUnknown {
					return nil, BinaryInvalidSourceError{
						targetName: "shared_library",
						sourceName: sourceName,
					}
				}

				// TODO: mergeable with cExecutableResolver?
				if sourceType == sourceH {
					continue
				}
				sourceBase := path.Base(sourceName)
				objFile, err := ctx.DeclareTool(
					"cxx",
					source,
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", outName, sourceBase),
					&graph.CompositeExpansions{
						Common: sharedExpansions,
						Elems: map[string][]string{
							// TODO: fill these out.
							"source_file_part": nil,
							"source_name_part": nil,
						},
					},
				)
				if err != nil {
					return nil, err
				}
				linkInput, err := objFile.AsSourceFile()
				if err != nil {
					return nil, err
				}
				linkInputs = append(linkInputs, linkInput)
			}
			libs, err := collectLibs(ctx)
			if err != nil {
				return nil, err
			}
			frameworks, err := collectFrameworks(ctx)
			if err != nil {
				return nil, err
			}
			out, err := ctx.DeclareTool(
				"solink",
				fs.SourceFile{},
				linkInputs,
				fmt.Sprintf("%s.so", outName),
				&graph.SimpleExpansions{Elems: map[string][]string{
					// TODO: fill these out.
					"ldflags":      ctx.ConfigValues.Ldflags,
					"libs":         formatLibExpansions(libs),
					"frameworks":   formatFrameworkExpansions(frameworks),
					"swiftmodules": nil,
				}},
			)
			if err != nil {
				return nil, err
			}
			return CxxInfo{
				DefaultMetadata: DefaultMetadata{[]fs.OutputPath{out}},
				LibFlags:        nil, // do not propagate libs
				Frameworks:      nil, // do not propagate frameworks
			}, nil
		},
	}
	SourceSetSchema = graph.Schema{
		Name:    "source_set",
		Summary: "Declare a source set target.",
		Vars: map[string]graph.TargetVar{
			// TODO: support more variables.
			"sources": graph.FileListVar{},
			"deps":    graph.LabelListVar{Expected: &graph.Target{}},
		},
	}
	StaticLibrarySchema = graph.Schema{
		Name:    "static_library",
		Summary: "Declare a shared library target.",
		Vars: map[string]graph.TargetVar{
			// TODO: support more variables.
			"sources":                graph.FileListVar{},
			"deps":                   graph.LabelListVar{Expected: &graph.Target{}},
			"output_prefix_override": graph.BoolVar{},
		},
		Resolver: func(ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
			name, err := ctx.StringFor("name")
			if err != nil {
				return nil, err
			}
			targetCategory := binaryUncategorized
			var linkInputs []fs.SourceFile
			outName := fmt.Sprintf("lib%s", name)
			override, err := ctx.BoolFor("output_prefix_override")
			if err == nil && override {
				outName = name
			}
			sharedExpansions := &graph.SimpleExpansions{Elems: map[string][]string{
				"cflags":  ctx.ConfigValues.Cflags,
				"defines": formatDefines(ctx.ConfigValues.Defines),
			}}
			for source := range ctx.SourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceType, category := fileTypeCategory(source.Filename())
				if targetCategory == binaryUncategorized {
					targetCategory = category
				} else if targetCategory != category {
					return nil, BinaryMixedSourcesError{}
				}
				if sourceType == sourceUnknown {
					return nil, BinaryInvalidSourceError{
						targetName: "static_library",
						sourceName: sourceName,
					}
				}

				// TODO: mergeable with cExecutableResolver?
				if sourceType == sourceH {
					continue
				}
				sourceBase := path.Base(sourceName)
				objFile, err := ctx.DeclareTool(
					"cxx",
					source,
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", outName, sourceBase),
					&graph.CompositeExpansions{
						Common: sharedExpansions,
						Elems: map[string][]string{
							// TODO: fill these out.
							"source_file_part": nil,
							"source_name_part": nil,
						},
					},
				)
				if err != nil {
					return nil, err
				}
				linkInput, err := objFile.AsSourceFile()
				if err != nil {
					return nil, err
				}
				linkInputs = append(linkInputs, linkInput)
			}
			libs, err := collectLibs(ctx)
			if err != nil {
				return nil, err
			}
			frameworks, err := collectFrameworks(ctx)
			if err != nil {
				return nil, err
			}
			out, err := ctx.DeclareTool(
				"alink",
				fs.SourceFile{},
				linkInputs,
				fmt.Sprintf("%s.a", outName),
				&graph.SimpleExpansions{Elems: map[string][]string{
					// TODO: add more.
					"arflags": ctx.ConfigValues.Arflags,
				}},
			)
			if err != nil {
				return nil, err
			}
			return CxxInfo{
				DefaultMetadata: DefaultMetadata{[]fs.OutputPath{out}},
				LibFlags:        libs,
				LibraryFiles:    []fs.OutputPath{out},
				Frameworks:      frameworks,
			}, nil
		},
	}
	GroupSchema = graph.Schema{
		Name:    "group",
		Summary: "Declare a named group of targets.",
		Vars: map[string]graph.TargetVar{
			// TODO: support more variables.
			"deps": graph.LabelListVar{Expected: &graph.Target{}},
		},
		Resolver: func(ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
			var outputs []fs.OutputPath
			for dep, err := range ctx.ResolvedTargetsFor("deps") {
				if err != nil {
					return nil, err
				}
				outputs = append(outputs, dep.Metadata.Outputs()...)
			}
			return DefaultMetadata{OutputPaths: outputs}, nil
		},
	}
)

func init() {
	maps.Copy(ExecutableSchema.Vars, graph.ConfigVars)
	maps.Copy(SharedLibrarySchema.Vars, graph.ConfigVars)
	maps.Copy(StaticLibrarySchema.Vars, graph.ConfigVars)
}
