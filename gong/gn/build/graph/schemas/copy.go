// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package schemas

import (
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

var CopySchema = graph.Schema{
	Name:    "copy",
	Summary: "Declare a target that copies files.",
	Vars: map[string]graph.TargetVar{
		// TODO: support more variables.
		"sources": graph.FileListVar{},
		"outputs": graph.StringListVar{},
	},
	Resolver: func(ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
		var outputs []fs.OutputPath

		var outputPattern string
		var gotPattern bool
		for pattern, err := range ctx.StringsFor("outputs") {
			// TODO: fix StringsFor to distinguish error because none and error from iter.
			// By doing so we should be able to get rid of this gotPattern hack.
			if err != nil || gotPattern {
				return nil, CopyBadOutputsError{}
			}
			outputPattern = pattern
			gotPattern = true
		}

		if !gotPattern {
			return nil, CopyBadOutputsError{}
		}

		for src, err := range ctx.SourceFilesFor("sources") {
			// TODO: fix SourceFilesFor to distinguish error because none and error from iter.
			// By doing so it should be clearer when we want to return CopyNoSourcesError.
			if err != nil {
				return nil, CopyNoSourcesError{}
			}
			out, err := ctx.DeclareTool("copy", src, nil, outputPattern, nil)
			if err != nil {
				return nil, err
			}
			outputs = append(outputs, out)
		}

		return DefaultMetadata{
			OutputPaths: outputs,
		}, nil
	},
}
