// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"fmt"
	"iter"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
)

// Target is an item in the GN dependency graph that represents a build target.
//
// Using their [Schema], targets are moved into a resolved state by the [Builder].
type Target struct {
	itemInfo
	Schema     *Schema
	values     map[string]processedValue
	Resolution Resolution
}

func (Target) compatibleWith(item Item) bool {
	switch item.(type) {
	case *Target:
		return true
	}
	return false
}

// ResolverContext provides context to a [ResolverFn], allowing only indirect access to underlying target data.
type ResolverContext struct {
	// DeclareTool declares a tool call.
	DeclareTool func(tool string, inputs []fs.SourceFile, outputName string) (fs.SourceFile, error)
	// StringFor returns the string for the variable, if it accepts strings.
	StringFor func(varName string) (string, error)
	// SourceFilesFor returns an iterator over source files for the variable, if it accepts file lists.
	SourceFilesFor func(varName string) iter.Seq2[fs.SourceFile, error]
	// ResolvedTargetsFor returns an iterator over resolutions for the variable, if it accepts target lists.
	ResolvedTargetsFor func(varName string) iter.Seq2[Resolution, error]
}

// A ResolverFn tries to resolve a target.
// It returns an error instead if processing fails.
type ResolverFn = func(ResolverContext) (fs.SourceFile, error)

// A Resolution of a target records the actions that a target performs, and any metadata that
// may be relevant to targets waiting for this target to be resolved.
//
// For now, the only metadata supported is a [fs.SourceFile] so deps can use it as input.
type Resolution struct {
	Actions []RunToolAction
	Output  fs.SourceFile
}

// A RunToolAction represents a call to a tool inside the current toolchain.
type RunToolAction struct {
	Tool   string
	Inputs []fs.SourceFile
	Output fs.SourceFile
}

func (t *Target) stringFor(varName string) (string, error) {
	v, ok := t.values[varName]
	if !ok {
		return "", fmt.Errorf("%s not declared", varName)
	}
	sv, err := processedValueAs[stringValue](v)
	if err != nil {
		return "", err
	}
	return sv.str, nil
}

func (t *Target) sourceFilesFor(varName string) iter.Seq2[fs.SourceFile, error] {
	return func(yield func(fs.SourceFile, error) bool) {
		v, ok := t.values[varName]
		if !ok {
			return
		}
		lv, err := processedValueAs[fileListValue](v)
		if err != nil {
			yield(fs.SourceFile{}, err)
			return
		}
		for _, sourceFile := range lv.list {
			if !yield(sourceFile, nil) {
				return
			}
		}
	}
}

func (t *Target) labelsFor(varName string) ([]environment.LabelWithOrigin, error) {
	v, ok := t.values[varName]
	if !ok {
		return nil, nil
	}
	llv, err := processedValueAs[labelListValue](v)
	if err != nil {
		return nil, err
	}
	return llv.list, nil
}

func (t *Target) declareTool(outDir fs.SourceDir, tool string, inputs []fs.SourceFile, outputName string) (fs.SourceFile, error) {
	outFile, err := outDir.ResolveRelativeFile(outputName)
	if err != nil {
		return fs.SourceFile{}, err
	}
	t.Resolution.Actions = append(t.Resolution.Actions, RunToolAction{
		Tool:   tool,
		Inputs: inputs,
		Output: outFile,
	})
	return outFile, nil
}

// LabelTargetPair represents a label, and a pointer to its target if that
// dependency has been resolved.
type LabelTargetPair struct {
	// Label is the label of the dependency.
	Label environment.Label
	// Origin is the parse node where this dependency was defined.
	Origin parse.Node
	// Target is the resolved target. This may be nil if the target has not
	// been resolved yet.
	Target *Target
}
