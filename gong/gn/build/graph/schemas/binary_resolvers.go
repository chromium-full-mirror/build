// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package schemas

import (
	"fmt"
	"os"
	"path"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

// TODO: can this be merged with shared_library, static_library?
func cExecutableResolver(name string, cInputs []fs.SourceFile, ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
	var linkInputs []fs.SourceFile
	for _, source := range cInputs {
		sourceName := source.Filename()
		sourceType, _ := fileTypeCategory(source.Filename())
		// TODO: check for other source types.
		if sourceType == sourceH {
			continue
		}
		sourceBase := path.Base(sourceName)
		objFile, err := ctx.DeclareTool(
			"cxx",
			source,
			[]fs.SourceFile{source},
			fmt.Sprintf("%s.%s.o", name, sourceBase),
		)
		if err != nil {
			return nil, err
		}
		linkInputs = append(linkInputs, objFile)
	}

	for dep, err := range ctx.ResolvedTargetsFor("deps") {
		if err != nil {
			return nil, err
		}
		for _, depOutput := range dep.Metadata.Outputs() {
			switch path.Ext(depOutput.Filename()) {
			case ".a", ".so":
				linkInputs = append(linkInputs, depOutput)
			default:
				// TODO: check for other dep input types.
				return nil, NotImplementedError{
					what: fmt.Sprintf("%q dep not implemented yet", depOutput.Filename()),
				}
			}
		}
	}

	out, err := ctx.DeclareTool("link", fs.SourceFile{}, linkInputs, name)
	if err != nil {
		return nil, err
	}

	return DefaultMetadata{[]fs.SourceFile{out}}, nil
}

func rustBinaryResolver(name string, isLibrary bool, rsInputs []fs.SourceFile, ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
	defaultCrateRoot := "main.rs"
	if isLibrary {
		defaultCrateRoot = "lib.rs"
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
	} else {
		for _, source := range rsInputs {
			if source.Base() == defaultCrateRoot {
				crateRoot = source
				foundCrateRoot = true
			}
		}
	}

	if !foundCrateRoot && len(rsInputs) == 1 {
		// If there's only one source, use that.
		crateRoot = rsInputs[0]
		foundCrateRoot = true
	}

	if !foundCrateRoot {
		return nil, CrateRootNotFoundError{
			expected: defaultCrateRoot,
		}
	}

	// Fetch aliased_deps if it exists, but ignore if it doesn't.
	aliasedDeps, _ := ctx.LabelKeyedStringMapFor("aliased_deps")

	var transitiveRlibs []fs.SourceFile
	for dep, err := range ctx.ResolvedTargetsFor("deps") {
		if err != nil {
			return nil, err
		}
		if rustLib, ok := dep.Metadata.(RustLibraryMetadata); ok {
			transitiveRlibs = append(transitiveRlibs, rustLib.OutputRlib)
			transitiveRlibs = append(transitiveRlibs, rustLib.TransitiveRlibs...)
		}
		if alias, ok := aliasedDeps[dep.Label]; ok {
			fmt.Fprintf(os.Stderr, "warning: alias not implemented yet. wanted to alias %s to %s\n", dep.Label.UserVisibleString(true), alias)
		}
	}

	allInputs := append(rsInputs, transitiveRlibs...)
	tool := "rust_bin"
	outputName := crateName
	if isLibrary {
		tool = "rust_rlib"
		outputName = fmt.Sprintf("lib%s.rlib", outputName)
	}
	out, err := ctx.DeclareTool(
		tool,
		crateRoot,
		allInputs,
		outputName,
	)
	if err != nil {
		return nil, err
	}

	if isLibrary {
		return RustLibraryMetadata{
			CrateName:       crateName,
			OutputRlib:      out,
			TransitiveRlibs: transitiveRlibs,
		}, nil
	}
	return DefaultMetadata{[]fs.SourceFile{out}}, nil
}
