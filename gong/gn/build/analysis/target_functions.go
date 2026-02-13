// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"fmt"
	"path/filepath"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

// A Schema is the type definition of a GN target.
//
// The word "schema" is an implementation detail; we're calling them "schemas" to avoid
// overloading the word "type" across both GN and Go contexts.
//
// User-facing documentation should still refer to GN target "types".
//
// The schema of a target specifies a name (e.g. "shared_library"), the types of variables
// it accepts (e.g. "sources", "deps"), and how it resolves a target definition.
type Schema struct {
	name     string
	summary  string
	vars     map[string]varType // TODO: Implement concept of required?
	resolver resolverFn
}

// TODO: add helper functions re file extensions like below
// https://source.chromium.org/gn/gn/+/main:src/gn/source_file.cc?q=SourceFile::SOURCE_H&ss=gn%2Fgn
var (
	actionSchema = Schema{
		name:    "action",
		summary: "Declare a target that runs a script a single time.",
		vars: map[string]varType{
			// TODO: support more variables.
			"script":  fileType,
			"sources": fileListType,
			"outputs": fileListType,
			"args":    stringListType,
			"depfile": stringType,
		},
	}
	executableSchema = Schema{
		name:    "executable",
		summary: "Declare an executable target.",
		vars: map[string]varType{
			// TODO: support more variables.
			"sources": fileListType,
			"deps":    targetLabelListType,
			"configs": configLabelListType,
			"outputs": fileListType,
		},
		resolver: func(ctx resolverContext) (fs.SourceFile, error) {
			name, err := ctx.stringFor("name")
			if err != nil {
				return fs.SourceFile{}, err
			}
			var linkInputs []fs.SourceFile
			for source := range ctx.sourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceBase := filepath.Base(sourceName)
				if filepath.Ext(sourceBase) == ".h" {
					continue
				}
				objFile, err := ctx.declareTool(
					"cxx",
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", name, sourceBase),
				)
				if err != nil {
					return fs.SourceFile{}, err
				}
				linkInputs = append(linkInputs, objFile)
			}
			for dep, err := range ctx.resolvedTargetsFor("deps") {
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
			return ctx.declareTool(
				"link",
				linkInputs,
				name,
			)
		},
	}
	sharedLibrarySchema = Schema{
		name:    "shared_library",
		summary: "Declare a shared library target.",
		vars: map[string]varType{
			// TODO: support more variables.
			"sources": fileListType,
			"deps":    targetLabelListType,
			"configs": configLabelListType,
			"defines": stringListType,
		},
		resolver: func(ctx resolverContext) (fs.SourceFile, error) {
			name, err := ctx.stringFor("name")
			if err != nil {
				return fs.SourceFile{}, err
			}
			var linkInputs []fs.SourceFile
			outPrefix := fmt.Sprintf("lib%s", name)
			for source := range ctx.sourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceBase := filepath.Base(sourceName)
				if filepath.Ext(sourceBase) == ".h" {
					continue
				}
				objFile, err := ctx.declareTool(
					"cxx",
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", outPrefix, sourceBase),
				)
				if err != nil {
					return fs.SourceFile{}, err
				}
				linkInputs = append(linkInputs, objFile)
			}
			return ctx.declareTool(
				"alink",
				linkInputs,
				fmt.Sprintf("%s.a", outPrefix),
			)
		},
	}
	sourceSetSchema = Schema{
		name:    "source_set",
		summary: "Declare a source set target.",
		vars: map[string]varType{
			// TODO: support more variables.
			"sources": fileListType,
			"deps":    targetLabelListType,
		},
	}
	staticLibrarySchema = Schema{
		name:    "static_library",
		summary: "Declare a shared library target.",
		vars: map[string]varType{
			// TODO: support more variables.
			"sources": fileListType,
			"deps":    targetLabelListType,
			"configs": configLabelListType,
			"defines": stringListType,
		},
		resolver: func(ctx resolverContext) (fs.SourceFile, error) {
			name, err := ctx.stringFor("name")
			if err != nil {
				return fs.SourceFile{}, err
			}
			var linkInputs []fs.SourceFile
			outPrefix := fmt.Sprintf("lib%s", name)
			for source := range ctx.sourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceBase := filepath.Base(sourceName)
				if filepath.Ext(sourceBase) == ".h" {
					continue
				}
				objFile, err := ctx.declareTool(
					"cxx",
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", outPrefix, sourceBase),
				)
				if err != nil {
					return fs.SourceFile{}, err
				}
				linkInputs = append(linkInputs, objFile)
			}
			return ctx.declareTool(
				"solink",
				linkInputs,
				fmt.Sprintf("%s.so", outPrefix),
			)
		},
	}
	copySchema = Schema{
		name:    "copy",
		summary: "Declare a target that copies files.",
		vars: map[string]varType{
			// TODO: support more variables.
			"sources": fileListType,
			"outputs": fileListType,
		},
	}
)

func (Schema) IsTarget() bool       { return true }
func (s *Schema) HelpShort() string { return fmt.Sprintf("%s: %s", s.name, s.summary) }
func (s *Schema) Help() string      { return s.HelpShort() } // TODO: support full description
func (s *Schema) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value, block *parse.BlockNode) (resolve.Value, error) {
	ctx, err := contextFromScope(scope)
	if err != nil {
		return nil, err
	}

	if ctx.isProcessingBuildConfig() {
		return nil, ItemInBuildConfigError{OriginFunction: resolve.OriginFunction{Call: call}}
	}

	if block == nil {
		return nil, fmt.Errorf("target definition missing block?")
	}

	blockScope := scope.NewNestedScope()

	// Set target_name.
	if len(args) == 0 {
		return nil, resolve.ArgumentCountError{
			OriginFunction: resolve.OriginFunction{Call: call},
			Msg:            "Target name is missing.",
		}
	}
	nameValue, err := resolve.AsValue[*resolve.StringValue](args[0])
	if err != nil {
		return nil, err
	}
	name := nameValue.RawGNString()

	if _, err := resolve.ExecuteNode(block, blockScope); err != nil {
		return nil, err
	}

	label := environment.Label{
		Dir:  ctx.sourceDir,
		Name: name,
		// TODO: Toolchain
	}

	target := &Target{
		itemInfo: itemInfo{
			label:       label,
			definedFrom: call,
		},
		schema:   s,
		settings: ctx.settings,
		values: map[string]processedValue{
			"name": stringValue{
				origin: nameValue,
				str:    name,
			},
		},
	}

	// Validate all of the target's values and perform initial processing.
	for acceptedVar, expectedType := range s.vars {
		value := blockScope.Value(acceptedVar, true)
		if value == nil {
			continue
		}
		processedValue, err := target.processValue(value, expectedType)
		if err != nil {
			return nil, err
		}
		target.values[acceptedVar] = processedValue
	}

	ctx.itemCollector(target)
	return nil, blockScope.CheckForUnusedVars()
}
