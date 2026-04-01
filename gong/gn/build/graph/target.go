// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"fmt"
	"iter"
	"path"
	"strings"

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

// BoolFor returns the boolean for the variable, if it accepts a boolean.
func (t *Target) BoolFor(varName string) (bool, error) {
	v, ok := t.Values[varName]
	if !ok {
		return false, fmt.Errorf("%s not declared", varName)
	}
	bv, err := ProcessedValueAs[BoolValue](v)
	if err != nil {
		return false, err
	}
	return bv.bool, nil
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

// StringsFor returns an iterator over strings for the variable, if it accepts string lists.
func (t *Target) StringsFor(varName string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		v, ok := t.Values[varName]
		if !ok {
			return
		}
		sv, err := ProcessedValueAs[StringListValue](v)
		if err != nil {
			yield("", err)
			return
		}
		for _, s := range sv.list {
			if !yield(s, nil) {
				return
			}
		}
	}
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
func (t *Target) DeclareTool(outDir fs.OutputPath, tool string, source fs.SourceFile, inputs []fs.SourceFile, outputName string, expansions map[string]string) (fs.OutputPath, error) {
	outPath := fs.MakeOutputPath(outDir.BuildDir(), path.Join(outDir.Path(), outputName))
	t.Resolution.Actions = append(t.Resolution.Actions, RunToolAction{
		Tool:       tool,
		Source:     source,
		Inputs:     inputs,
		Output:     outPath,
		Expansions: expansions,
	})
	return outPath, nil
}

// DeclareScript declares a script call.
func (t *Target) DeclareScript(outDir fs.OutputPath, script fs.SourceFile, args []string, outputNames []string, inputs []fs.SourceFile, depfile string, rspfileContent []string) ([]fs.OutputPath, error) {
	var parsedArgs []SubstitutionPattern
	for _, arg := range args {
		parsed, err := makeSubstitutionPattern(arg)
		if err != nil {
			return nil, err
		}
		parsedArgs = append(parsedArgs, parsed)
	}
	var parsedRsp []SubstitutionPattern
	for _, r := range rspfileContent {
		parsed, err := makeSubstitutionPattern(r)
		if err != nil {
			return nil, err
		}
		parsedRsp = append(parsedRsp, parsed)
	}
	var outputs []fs.OutputPath
	for _, name := range outputNames {
		outputs = append(outputs, fs.MakeOutputPath(outDir.BuildDir(), path.Join(outDir.Path(), name)))
	}
	t.Resolution.Actions = append(t.Resolution.Actions, RunScriptAction{
		Script:         script,
		Args:           parsedArgs,
		Outputs:        outputs,
		Inputs:         inputs,
		Depfile:        depfile,
		RspfileContent: parsedRsp,
	})
	return outputs, nil
}

// OutDir returns the output directory for this target.
func (t *Target) OutDir(buildSettings *environment.BuildSettings) fs.OutputPath {
	// The source dir is source-absolute, so we trim off the two leading
	// slashes to append to the toolchain object directory.
	targetAsPath := strings.TrimPrefix(t.Label().Dir.Path(), "//")
	// TODO: Placeholder implementation that always assumes obj/.
	// To be correct, we need to also support absolute paths, support gen/, support phony/, etc.
	//
	// TODO: Alternatively, this path should be considered implementation detail of ninjawriter.
	// Right now this is not possible because this outdir is then used to create fs.SourceFile
	// so that we can use outputs as intermediate sources.
	// Hence we would need to have some kind of "artifact" struct to be able to represent both
	// sources and intermediate outputs without needing to have a hard dep on the output path.
	// Then, ninjawriter can decide where to put intermediate outputs.
	return fs.MakeOutputPath(buildSettings.BuildDir, path.Join("obj", targetAsPath))
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
