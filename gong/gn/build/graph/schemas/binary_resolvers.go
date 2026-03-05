// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package schemas

import (
	"fmt"
	"path"
	"strings"

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
			map[string]string{
				// TODO: fill these out.
				"source_file_part": "",
				"source_name_part": "",
				"cflags":           strings.Join(ctx.ConfigValues.Cflags, " "),
			},
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

	out, err := ctx.DeclareTool(
		"link",
		fs.SourceFile{},
		linkInputs,
		name,
		map[string]string{
			// TODO: fill these out.
			"ldflags":      strings.Join(ctx.ConfigValues.Ldflags, " "),
			"libs":         "",
			"frameworks":   "",
			"swiftmodules": "",
		},
	)
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

	var externs []string
	var transitiveRlibs []fs.SourceFile
	for dep, err := range ctx.ResolvedTargetsFor("deps") {
		if err != nil {
			return nil, err
		}
		if rustLib, ok := dep.Metadata.(RustLibraryMetadata); ok {
			transitiveRlibs = append(transitiveRlibs, rustLib.OutputRlib)
			transitiveRlibs = append(transitiveRlibs, rustLib.TransitiveRlibs...)
			depCrateName := rustLib.CrateName
			if alias, ok := aliasedDeps[dep.Label]; ok {
				depCrateName = alias
			}
			// TODO: maybe it's not filename? see test files for why this seems wrong.
			externs = append(externs, fmt.Sprintf("--extern %s=%s", depCrateName, rustLib.OutputRlib.Filename()))
		}
	}

	allInputs := append(rsInputs, transitiveRlibs...)
	tool := "rust_bin"
	crateType := "bin"
	outputName := crateName
	if isLibrary {
		tool = "rust_rlib"
		crateType = "rlib"
		outputName = fmt.Sprintf("lib%s.rlib", outputName)
	}
	out, err := ctx.DeclareTool(
		tool,
		crateRoot,
		allInputs,
		outputName,
		map[string]string{
			"crate_name": crateName,
			"crate_type": crateType,
			"externs":    strings.Join(externs, " "),
			"rustflags":  strings.Join(ctx.ConfigValues.Rustflags, " "),
			"rustdeps":   "",
		},
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
