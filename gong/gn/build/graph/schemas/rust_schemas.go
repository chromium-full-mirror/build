// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package schemas

import (
	"fmt"

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
		"configs":    graph.LabelListVar{Expected: &graph.Config{}},
		"defines":    graph.StringListVar{},
	},
	Resolver: func(ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
		name, err := ctx.StringFor("name")
		if err != nil {
			return nil, err
		}

		crateName, err := ctx.StringFor("crate_name")
		if err != nil {
			// Fall back to target name.
			crateName = name
		}

		foundCrateRoot := false
		crateRoot, err := ctx.SourceFileFor("crate_root")
		if err == nil {
			foundCrateRoot = true
		}

		var inputs []fs.SourceFile
		for source, err := range ctx.SourceFilesFor("sources") {
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, source)
			// See if "lib.rs" is in sources.
			if !foundCrateRoot && source.Base() == "lib.rs" {
				crateRoot = source
				foundCrateRoot = true
			}
		}

		if !foundCrateRoot && len(inputs) == 1 {
			// If there's only one source, use that.
			crateRoot = inputs[0]
			foundCrateRoot = true
		}

		if !foundCrateRoot {
			return nil, CrateRootNotFoundError{
				expected: "lib.rs",
			}
		}

		var transitiveRlibs []fs.SourceFile
		for dep, err := range ctx.ResolvedTargetsFor("deps") {
			if err != nil {
				return nil, err
			}
			if rustLib, ok := dep.Metadata.(RustLibraryMetadata); ok {
				transitiveRlibs = append(transitiveRlibs, rustLib.OutputRlib)
				transitiveRlibs = append(transitiveRlibs, rustLib.TransitiveRlibs...)
			}
		}
		inputs = append(inputs, transitiveRlibs...)

		out, err := ctx.DeclareTool(
			"rust_rlib",
			crateRoot,
			inputs,
			fmt.Sprintf("lib%s.rlib", crateName),
		)
		if err != nil {
			return nil, err
		}

		return RustLibraryMetadata{
			CrateName:       crateName,
			OutputRlib:      out,
			TransitiveRlibs: transitiveRlibs,
		}, nil
	},
}
