// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

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
	ItemInfo
	Schema     *Schema
	Values     map[string]ProcessedValue
	Resolution Resolution
}

// CompatibleWith checks whether the other item is also a *Target.
func (Target) CompatibleWith(item Item) bool {
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

// StringFor returns the string for the variable, if it accepts strings.
func (t *Target) StringFor(varName string) (string, error) {
	v, ok := t.Values[varName]
	if !ok {
		return "", fmt.Errorf("%s not declared", varName)
	}
	sv, err := ProcessedValueAs[StringValue](v)
	if err != nil {
		return "", err
	}
	return sv.str, nil
}

// SourceFilesFor returns an iterator over source files for the variable, if it accepts file lists.
func (t *Target) SourceFilesFor(varName string) iter.Seq2[fs.SourceFile, error] {
	return func(yield func(fs.SourceFile, error) bool) {
		v, ok := t.Values[varName]
		if !ok {
			return
		}
		lv, err := ProcessedValueAs[FileListValue](v)
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

// LabelsFor returns an iterator over labels for the variable, if it accepts item (target, config, etc.) lists.
func (t *Target) LabelsFor(varName string) ([]environment.LabelWithOrigin, error) {
	v, ok := t.Values[varName]
	if !ok {
		return nil, nil
	}
	llv, err := ProcessedValueAs[LabelListValue](v)
	if err != nil {
		return nil, err
	}
	return llv.List, nil
}

// DeclareTool declares a tool call.
func (t *Target) DeclareTool(outDir fs.SourceDir, tool string, inputs []fs.SourceFile, outputName string) (fs.SourceFile, error) {
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
