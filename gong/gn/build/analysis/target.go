// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"fmt"
	"iter"
	"os"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
)

// Target is an item in the GN dependency graph that represents a build target.
//
// Using their [Schema], targets are moved into a resolved state by the [Builder].
type Target struct {
	itemInfo
	settings   *Settings
	schema     *Schema
	values     map[string]processedValue
	resolution resolution
}

func (Target) compatibleWith(item Item) bool {
	switch item.(type) {
	case *Target:
		return true
	}
	return false
}

// A resolverFn tries to resolve a target.
// It returns an error instead if processing fails.
type resolverFn = func(*Target, *Builder) (fs.SourceFile, error)

// A resolution of a target records the actions that a target performs, and any metadata that
// may be relevant to targets waiting for this target to be resolved.
//
// For now, the only metadata supported is a [fs.SourceFile] so deps can use it as input.
type resolution struct {
	actions []runToolAction
	output  fs.SourceFile
}

type runToolAction struct {
	tool   string
	inputs []fs.SourceFile
	output fs.SourceFile
}

func (t *Target) resolve(b *Builder) (fs.SourceFile, error) {
	if t.schema == nil {
		return fs.SourceFile{}, environment.IllegalStateError{
			Reason: "Attempted to resolve target without schema",
		}
	}
	if t.schema.resolver == nil {
		fmt.Fprintf(os.Stderr, "ignoring target %v for now since no resolver...\n", t.label.UserVisibleString(false))
		return fs.SourceFile{}, nil
	}
	return t.schema.resolver(t, b)
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

func (t *Target) resolvedTargetsFor(varName string, b *Builder) iter.Seq2[resolution, error] {
	v, ok := t.values[varName]
	if !ok {
		return nil
	}
	return func(yield func(resolution, error) bool) {
		lv, err := processedValueAs[labelListValue](v)
		if err != nil {
			yield(resolution{}, err)
			return
		}
		for _, dep := range lv.list {
			depRecord, err := b.recordFor(dep.Label, dep.Origin, &Target{})
			if err != nil {
				yield(resolution{}, err)
				return
			}
			if depRecord.state != itemStateResolved {
				yield(resolution{}, environment.IllegalStateError{Reason: "unresolved dep found"})
				return
			}
			switch t := depRecord.item.(type) {
			case *Target:
				if !yield(t.resolution, nil) {
					return
				}
			default:
				yield(resolution{}, ItemTypeMismatchError{
					OriginNode:        parse.OriginNode{Node: dep.Origin},
					label:             t.Label(),
					itemOrPlaceholder: &Target{},
					existingRecord:    depRecord,
				})
				return
			}
		}
	}
}

func (t *Target) declareTool(tool string, inputs []fs.SourceFile, outputName string) (fs.SourceFile, error) {
	outDir, err := t.buildDirAsSourceDir()
	if err != nil {
		return fs.SourceFile{}, err
	}
	outFile, err := outDir.ResolveRelativeFile(outputName)
	if err != nil {
		return fs.SourceFile{}, err
	}
	t.resolution.actions = append(t.resolution.actions, runToolAction{
		tool:   tool,
		inputs: inputs,
		output: outFile,
	})
	return outFile, nil
}

// buildDirAsSourceDir returns the output or generated file directory corresponding to the given
// target.
//
// TODO: This is a placeholder implementation that always assumes obj/.
// To be correct, we need to also support absolute paths, support gen/, support phony/, etc.
func (t *Target) buildDirAsSourceDir() (fs.SourceDir, error) {
	return t.settings.buildSettings.BuildDir.ResolveRelativeDir("obj/" + t.label.Dir.Path())
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
