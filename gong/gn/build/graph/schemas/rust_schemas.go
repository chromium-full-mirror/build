// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package schemas

import (
	"maps"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

// RustLibraryMetadata is a rudimentary implementation of metadata that
// is returned by rust_library targets.
type RustLibraryMetadata struct {
	// CrateName is the crate name for this target.
	CrateName string
	// OutputRlib is the .rlib produced by this target.
	OutputRlib fs.SourceFile
	// TransitiveRlibs collects the transitive rlibs needed by executable().
	// TODO: maybe use map[fs.SourceFile]struct{} instead?
	TransitiveRlibs []fs.SourceFile
}

// Outputs returns the output(s) from this target.
func (m RustLibraryMetadata) Outputs() []fs.SourceFile {
	return []fs.SourceFile{m.OutputRlib}
}

var RustLibrarySchema = graph.Schema{
	Name:    "rust_library",
	Summary: "Declare a Rust library target.",
	Vars: map[string]graph.TargetVar{
		// TODO: support more variables.
		"sources":    graph.FileListVar{},
		"crate_name": graph.StringVar{},
		"crate_root": graph.FileVar{},
		"deps":       graph.LabelListVar{Expected: &graph.Target{}},
	},
	Resolver: func(ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
		name, err := ctx.StringFor("name")
		if err != nil {
			return nil, err
		}
		var inputs []fs.SourceFile
		for source, err := range ctx.SourceFilesFor("sources") {
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, source)
		}
		return rustBinaryResolver(name, true, inputs, ctx)
	},
}

func init() {
	maps.Copy(RustLibrarySchema.Vars, graph.ConfigVars)
}
