// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package schemas defines target implementations.
// The word "schema" is an implementation detail specific to gong.
package schemas

import (
	"fmt"
	"path/filepath"

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
	sourceSwiftModule
)

// Naive port of SourceFile::GetSourceFileType that can very likely be optimized.
// That's not really a priority to look into right now, though.
// https://source.chromium.org/gn/gn/+/main:src/gn/source_file.cc;l=32;drc=487f8353f15456474437df32bb186187b0940b45
func fileType(file string) sourceFileType {
	switch filepath.Ext(file) {
	case ".c":
		return sourceC
	case ".h":
		return sourceH
	case ".m":
		return sourceM
	case ".o", ".obj":
		return sourceO
	case ".S", ".s", ".asm":
		return sourceS
	case ".cc", ".cxx", ".cpp", ".c++":
		return sourceCpp
	case ".go":
		return sourceGo
	case ".hh", ".hpp", ".hpp11", ".hxx", ".inc", ".ipp", ".inl":
		return sourceH
	case ".mm":
		return sourceMm
	case ".rc":
		return sourceRc
	case ".rs":
		return sourceRs
	case ".def":
		return sourceDef
	case ".swift":
		return sourceSwift
	case ".swiftmodule":
		return sourceSwiftModule
	case ".modulemap":
		return sourceModuleMap
	default:
		return sourceUnknown
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
			var linkInputs []fs.SourceFile
			for source := range ctx.SourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceBase := filepath.Base(sourceName)
				if fileType(sourceBase) == sourceH {
					continue
				}
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
					switch filepath.Ext(depOutput.Filename()) {
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
			var linkInputs []fs.SourceFile
			outPrefix := fmt.Sprintf("lib%s", name)
			for source := range ctx.SourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceBase := filepath.Base(sourceName)
				if fileType(sourceBase) == sourceH {
					continue
				}
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
			var linkInputs []fs.SourceFile
			outPrefix := fmt.Sprintf("lib%s", name)
			for source := range ctx.SourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceBase := filepath.Base(sourceName)
				if fileType(sourceBase) == sourceH {
					continue
				}
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
