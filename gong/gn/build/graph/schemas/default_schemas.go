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

// TODO: add helper functions re file extensions like below
// https://source.chromium.org/gn/gn/+/main:src/gn/source_file.cc?q=SourceFile::SOURCE_H&ss=gn%2Fgn
var (
	ActionSchema = graph.Schema{
		Name:    "action",
		Summary: "Declare a target that runs a script a single time.",
		Vars: map[string]graph.VarType{
			// TODO: support more variables.
			"script":  graph.FileType,
			"sources": graph.FileListType,
			"outputs": graph.FileListType,
			"args":    graph.StringListType,
			"depfile": graph.StringType,
		},
	}
	ExecutableSchema = graph.Schema{
		Name:    "executable",
		Summary: "Declare an executable target.",
		Vars: map[string]graph.VarType{
			// TODO: support more variables.
			"sources": graph.FileListType,
			"deps":    graph.TargetLabelListType,
			"configs": graph.ConfigLabelListType,
			"outputs": graph.FileListType,
		},
		Resolver: func(ctx graph.ResolverContext) (fs.SourceFile, error) {
			name, err := ctx.StringFor("name")
			if err != nil {
				return fs.SourceFile{}, err
			}
			var linkInputs []fs.SourceFile
			for source := range ctx.SourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceBase := filepath.Base(sourceName)
				if filepath.Ext(sourceBase) == ".h" {
					continue
				}
				objFile, err := ctx.DeclareTool(
					"cxx",
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", name, sourceBase),
				)
				if err != nil {
					return fs.SourceFile{}, err
				}
				linkInputs = append(linkInputs, objFile)
			}
			for dep, err := range ctx.ResolvedTargetsFor("deps") {
				if err != nil {
					return fs.SourceFile{}, err
				}
				switch filepath.Ext(dep.Output.Filename()) {
				case ".a":
				case ".so":
					linkInputs = append(linkInputs, dep.Output)
				default:
					return fs.SourceFile{}, NotImplementedError{
						what: fmt.Sprintf("%q dep not implemented yet", dep.Output.Filename()),
					}
				}
			}
			return ctx.DeclareTool(
				"link",
				linkInputs,
				name,
			)
		},
	}
	SharedLibrarySchema = graph.Schema{
		Name:    "shared_library",
		Summary: "Declare a shared library target.",
		Vars: map[string]graph.VarType{
			// TODO: support more variables.
			"sources": graph.FileListType,
			"deps":    graph.TargetLabelListType,
			"configs": graph.ConfigLabelListType,
			"defines": graph.StringListType,
		},
		Resolver: func(ctx graph.ResolverContext) (fs.SourceFile, error) {
			name, err := ctx.StringFor("name")
			if err != nil {
				return fs.SourceFile{}, err
			}
			var linkInputs []fs.SourceFile
			outPrefix := fmt.Sprintf("lib%s", name)
			for source := range ctx.SourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceBase := filepath.Base(sourceName)
				if filepath.Ext(sourceBase) == ".h" {
					continue
				}
				objFile, err := ctx.DeclareTool(
					"cxx",
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", outPrefix, sourceBase),
				)
				if err != nil {
					return fs.SourceFile{}, err
				}
				linkInputs = append(linkInputs, objFile)
			}
			return ctx.DeclareTool(
				"solink",
				linkInputs,
				fmt.Sprintf("%s.so", outPrefix),
			)
		},
	}
	SourceSetSchema = graph.Schema{
		Name:    "source_set",
		Summary: "Declare a source set target.",
		Vars: map[string]graph.VarType{
			// TODO: support more variables.
			"sources": graph.FileListType,
			"deps":    graph.TargetLabelListType,
		},
	}
	StaticLibrarySchema = graph.Schema{
		Name:    "static_library",
		Summary: "Declare a shared library target.",
		Vars: map[string]graph.VarType{
			// TODO: support more variables.
			"sources": graph.FileListType,
			"deps":    graph.TargetLabelListType,
			"configs": graph.ConfigLabelListType,
			"defines": graph.StringListType,
		},
		Resolver: func(ctx graph.ResolverContext) (fs.SourceFile, error) {
			name, err := ctx.StringFor("name")
			if err != nil {
				return fs.SourceFile{}, err
			}
			var linkInputs []fs.SourceFile
			outPrefix := fmt.Sprintf("lib%s", name)
			for source := range ctx.SourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceBase := filepath.Base(sourceName)
				if filepath.Ext(sourceBase) == ".h" {
					continue
				}
				objFile, err := ctx.DeclareTool(
					"cxx",
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", outPrefix, sourceBase),
				)
				if err != nil {
					return fs.SourceFile{}, err
				}
				linkInputs = append(linkInputs, objFile)
			}
			return ctx.DeclareTool(
				"alink",
				linkInputs,
				fmt.Sprintf("%s.a", outPrefix),
			)
		},
	}
	CopySchema = graph.Schema{
		Name:    "copy",
		Summary: "Declare a target that copies files.",
		Vars: map[string]graph.VarType{
			// TODO: support more variables.
			"sources": graph.FileListType,
			"outputs": graph.FileListType,
		},
	}
)
