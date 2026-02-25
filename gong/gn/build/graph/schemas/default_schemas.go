// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package schemas defines target implementations.
// The word "schema" is an implementation detail specific to gong.
package schemas

import (
	"fmt"
	"path"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

// DefaultMetadata is a minimal implementation of resolution metadata
// that only provides outputs of the target.
type DefaultMetadata struct {
	OutputFiles []fs.SourceFile
}

// Outputs returns the output(s) from this target.
func (m DefaultMetadata) Outputs() []fs.SourceFile {
	return m.OutputFiles
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

var (
	ActionSchema = graph.Schema{
		Name:    "action",
		Summary: "Declare a target that runs a script a single time.",
		Vars: map[string]graph.TargetVar{
			// TODO: support more variables.
			"script":  graph.FileVar{},
			"sources": graph.FileListVar{},
			"outputs": graph.FileListVar{},
			"args":    graph.StringListVar{},
			"depfile": graph.StringVar{},
		},
	}
	ExecutableSchema = graph.Schema{
		Name:    "executable",
		Summary: "Declare an executable target.",
		Vars: map[string]graph.TargetVar{
			// TODO: support more variables.
			"sources": graph.FileListVar{},
			"deps":    graph.LabelListVar{Expected: &graph.Target{}},
			"configs": graph.LabelListVar{Expected: &graph.Config{}},
			"outputs": graph.FileListVar{},
		},
		Resolver: func(ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
			name, err := ctx.StringFor("name")
			if err != nil {
				return nil, err
			}
			targetCategory := binaryUncategorized
			var linkInputs []fs.SourceFile
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
						targetName: "executable",
						sourceName: sourceName,
					}
				}

				// TODO: move below into e.g. new file "c_schemas.go"?
				// (Hence don't bother merging with above if into a switch.)
				if sourceType == sourceH {
					continue
				}
				sourceBase := path.Base(sourceName)
				objFile, err := ctx.DeclareTool(
					"cxx",
					source,
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", name, sourceBase),
				)
				if err != nil {
					return nil, err
				}
				linkInputs = append(linkInputs, objFile)
			}
			for dep, err := range ctx.ResolvedTargetsFor("deps") {
				if err != nil {
					return nil, err
				}
				for _, depOutput := range dep.Metadata.Outputs() {
					switch path.Ext(depOutput.Filename()) {
					case ".a":
					case ".so":
						linkInputs = append(linkInputs, depOutput)
					default:
						return nil, NotImplementedError{
							what: fmt.Sprintf("%q dep not implemented yet", depOutput.Filename()),
						}
					}
				}
			}
			out, err := ctx.DeclareTool("link", fs.SourceFile{}, linkInputs, name)
			if err != nil {
				return nil, err
			}
			return DefaultMetadata{[]fs.SourceFile{out}}, nil
		},
	}
	SharedLibrarySchema = graph.Schema{
		Name:    "shared_library",
		Summary: "Declare a shared library target.",
		Vars: map[string]graph.TargetVar{
			// TODO: support more variables.
			"sources": graph.FileListVar{},
			"deps":    graph.LabelListVar{Expected: &graph.Target{}},
			"configs": graph.LabelListVar{Expected: &graph.Config{}},
			"defines": graph.StringListVar{},
		},
		Resolver: func(ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
			name, err := ctx.StringFor("name")
			if err != nil {
				return nil, err
			}
			targetCategory := binaryUncategorized
			var linkInputs []fs.SourceFile
			outPrefix := fmt.Sprintf("lib%s", name)
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

				// TODO: move below into e.g. new file "c_schemas.go"?
				// (Hence don't bother merging with above if into a switch.)
				if sourceType == sourceH {
					continue
				}
				sourceBase := path.Base(sourceName)
				objFile, err := ctx.DeclareTool(
					"cxx",
					source,
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", outPrefix, sourceBase),
				)
				if err != nil {
					return nil, err
				}
				linkInputs = append(linkInputs, objFile)
			}
			out, err := ctx.DeclareTool(
				"solink",
				fs.SourceFile{},
				linkInputs,
				fmt.Sprintf("%s.so", outPrefix),
			)
			if err != nil {
				return nil, err
			}
			return DefaultMetadata{[]fs.SourceFile{out}}, nil
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
			"sources": graph.FileListVar{},
			"deps":    graph.LabelListVar{Expected: &graph.Target{}},
			"configs": graph.LabelListVar{Expected: &graph.Config{}},
			"defines": graph.StringListVar{},
		},
		Resolver: func(ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
			name, err := ctx.StringFor("name")
			if err != nil {
				return nil, err
			}
			targetCategory := binaryUncategorized
			var linkInputs []fs.SourceFile
			outPrefix := fmt.Sprintf("lib%s", name)
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

				// TODO: move below into e.g. new file "c_schemas.go"?
				// (Hence don't bother merging with above if into a switch.)
				if sourceType == sourceH {
					continue
				}
				sourceBase := path.Base(sourceName)
				objFile, err := ctx.DeclareTool(
					"cxx",
					source,
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", outPrefix, sourceBase),
				)
				if err != nil {
					return nil, err
				}
				linkInputs = append(linkInputs, objFile)
			}
			out, err := ctx.DeclareTool(
				"alink",
				fs.SourceFile{},
				linkInputs,
				fmt.Sprintf("%s.a", outPrefix),
			)
			if err != nil {
				return nil, err
			}
			return DefaultMetadata{[]fs.SourceFile{out}}, nil
		},
	}
	CopySchema = graph.Schema{
		Name:    "copy",
		Summary: "Declare a target that copies files.",
		Vars: map[string]graph.TargetVar{
			// TODO: support more variables.
			"sources": graph.FileListVar{},
			"outputs": graph.FileListVar{},
		},
	}
)
