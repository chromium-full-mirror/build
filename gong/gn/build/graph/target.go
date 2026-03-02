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

// LabelKeyedStringMapFor returns the map of labels to strings for the variable, if it accepts a variable
// that is processed into a map of labels to strings.
func (t *Target) LabelKeyedStringMapFor(varName string) (map[environment.Label]string, error) {
	v, ok := t.Values[varName]
	if !ok {
		return nil, fmt.Errorf("%s not declared", varName)
	}
	mv, err := ProcessedValueAs[LabelKeyedStringMapValue](v)
	if err != nil {
		return nil, err
	}
	return mv.data, nil
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

// SourceFileFor returns the source file for the variable, if it accepts a file.
func (t *Target) SourceFileFor(varName string) (fs.SourceFile, error) {
	v, ok := t.Values[varName]
	if !ok {
		return fs.SourceFile{}, fmt.Errorf("%s not declared", varName)
	}
	fv, err := ProcessedValueAs[FileValue](v)
	if err != nil {
		return fs.SourceFile{}, err
	}
	return fv.file, nil
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
func (t *Target) DeclareTool(outDir fs.SourceDir, tool string, source fs.SourceFile, inputs []fs.SourceFile, outputName string, expansions map[string]string) (fs.SourceFile, error) {
	outFile, err := outDir.ResolveRelativeFile(outputName)
	if err != nil {
		return fs.SourceFile{}, err
	}
	t.Resolution.Actions = append(t.Resolution.Actions, RunToolAction{
		Tool:       tool,
		Source:     source,
		Inputs:     inputs,
		Output:     outFile,
		Expansions: expansions,
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
