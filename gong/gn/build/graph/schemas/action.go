// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package schemas

import (
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

var ActionSchema = graph.Schema{
	Name:    "action",
	Summary: "Declare a target that runs a script a single time.",
	Vars: map[string]graph.TargetVar{
		// TODO: support more variables.
		"script":  graph.FileVar{},
		"inputs":  graph.FileListVar{},
		"sources": graph.FileListVar{},
		"outputs": graph.FileListVar{},
		"args":    graph.StringListVar{},
		"depfile": graph.StringVar{},
	},
	Resolver: func(ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
		script, err := ctx.SourceFileFor("script")
		if err != nil {
			return nil, ActionMissingScriptError{}
		}

		var args []string
		for arg, err := range ctx.StringsFor("args") {
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
		}

		var inputs []fs.SourceFile
		for f, err := range ctx.SourceFilesFor("inputs") {
			if err != nil {
				// TODO: fix SourceFilesFor to distinguish error because none and error from iter.
				break
			}
			inputs = append(inputs, f)
		}
		for f, err := range ctx.SourceFilesFor("sources") {
			if err != nil {
				// TODO: fix SourceFilesFor to distinguish error because none and error from iter.
				break
			}
			inputs = append(inputs, f)
		}

		var outputNames []string
		for f, err := range ctx.SourceFilesFor("outputs") {
			if err != nil {
				return nil, err
			}
			outputNames = append(outputNames, f.Filename())
		}

		depfile, _ := ctx.StringFor("depfile")

		outputs, err := ctx.DeclareScript(script, args, outputNames, inputs, depfile)
		if err != nil {
			return nil, err
		}

		return DefaultMetadata{OutputPaths: outputs}, nil
	},
}
