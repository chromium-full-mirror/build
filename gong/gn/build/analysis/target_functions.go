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

// varType is the type of a target's variable.
type varType uint8

const (
	stringType = iota
	stringListType
	targetLabelListType
	configLabelListType
	fileType
	fileListType
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
		resolver: func(t *Target, b *Builder) (fs.SourceFile, error) {
			name, err := t.stringFor("name")
			if err != nil {
				return fs.SourceFile{}, err
			}
			var linkInputs []fs.SourceFile
			for source := range t.sourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceBase := filepath.Base(sourceName)
				if filepath.Ext(sourceBase) == ".h" {
					continue
				}
				objFile, err := t.declareTool(
					"cxx",
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", name, sourceBase),
				)
				if err != nil {
					return fs.SourceFile{}, err
				}
				linkInputs = append(linkInputs, objFile)
			}
			for dep, err := range t.resolvedTargetsFor("deps", b) {
				if err != nil {
					return fs.SourceFile{}, err
				}
				switch filepath.Ext(dep.output.Filename()) {
				case ".a":
				case ".so":
					linkInputs = append(linkInputs, dep.output)
				default:
					return fs.SourceFile{}, NotImplementedError{
						what: fmt.Sprintf("%q dep not implemented yet", dep.output.Filename()),
					}
				}
			}
			return t.declareTool(
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
		resolver: func(t *Target, b *Builder) (fs.SourceFile, error) {
			name, err := t.stringFor("name")
			if err != nil {
				return fs.SourceFile{}, err
			}
			var linkInputs []fs.SourceFile
			outPrefix := fmt.Sprintf("lib%s", name)
			for source := range t.sourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceBase := filepath.Base(sourceName)
				if filepath.Ext(sourceBase) == ".h" {
					continue
				}
				objFile, err := t.declareTool(
					"cxx",
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", outPrefix, sourceBase),
				)
				if err != nil {
					return fs.SourceFile{}, err
				}
				linkInputs = append(linkInputs, objFile)
			}
			return t.declareTool(
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
		resolver: func(t *Target, b *Builder) (fs.SourceFile, error) {
			name, err := t.stringFor("name")
			if err != nil {
				return fs.SourceFile{}, err
			}
			var linkInputs []fs.SourceFile
			outPrefix := fmt.Sprintf("lib%s", name)
			for source := range t.sourceFilesFor("sources") {
				sourceName := source.Filename()
				sourceBase := filepath.Base(sourceName)
				if filepath.Ext(sourceBase) == ".h" {
					continue
				}
				objFile, err := t.declareTool(
					"cxx",
					[]fs.SourceFile{source},
					fmt.Sprintf("%s.%s.o", outPrefix, sourceBase),
				)
				if err != nil {
					return fs.SourceFile{}, err
				}
				linkInputs = append(linkInputs, objFile)
			}
			return t.declareTool(
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

	if _, err := resolve.ExecuteNode(block, blockScope); err != nil {
		return nil, err
	}

	label := environment.Label{
		Dir:  ctx.sourceDir,
		Name: nameValue.RawGNString(),
		// TODO: Toolchain
	}

	target := &Target{
		itemInfo: itemInfo{
			label:       label,
			definedFrom: call,
		},
		schema:   s,
		settings: ctx.settings,
		values: map[string]resolve.Value{
			"name": nameValue,
		},
	}

	// Check all variables passed to the target are of the expected type.
	// TODO: If we only validate but don't try to convert into Label, fs.SourceFile etc.
	// then it's not great that later these values need to be checked and converted from
	// string values into concrete references twice - once by the Builder to find deps,
	// then by the resolve function *again* when it needs to read deps.
	// Should we try to resolve targets, fs.SourceFile, etc and save them on
	// the *Target here?
	for acceptedVar, expectedType := range s.vars {
		value := blockScope.Value(acceptedVar, true)
		if value == nil {
			continue
		}
		switch expectedType {
		case stringType,
			fileType:
			if _, err := resolve.AsValue[*resolve.StringValue](value); err != nil {
				return nil, err
			}
		case stringListType,
			fileListType,
			// The Builder is responsible for reading label lists to find dependencies
			// and validating the labels are correctly formatted.
			targetLabelListType,
			configLabelListType:
			if _, err := resolve.AsValue[*resolve.ListValue](value); err != nil {
				return nil, err
			}
		default:
			return nil, environment.IllegalStateError{
				Reason: "non-exhaustive switch over target variable types",
			}
		}
		target.values[acceptedVar] = value
	}

	ctx.itemCollector(target)
	return nil, blockScope.CheckForUnusedVars()
}
