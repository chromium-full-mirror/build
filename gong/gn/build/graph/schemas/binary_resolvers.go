// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package schemas

import (
	"fmt"
	"path"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

// TODO: can this be merged with shared_library, static_library?
func cExecutableResolver(name string, cInputs []fs.SourceFile, ctx graph.ResolverContext) (graph.ResolutionMetadata, error) {
	sharedExpansions := &graph.SimpleExpansions{Elems: map[string][]string{
		"cflags":  ctx.ConfigValues.Cflags,
		"defines": formatDefines(ctx.ConfigValues.Defines),
	}}
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
			&graph.CompositeExpansions{
				Common: sharedExpansions,
				Elems: map[string][]string{
					// TODO: fill these out.
					"source_file_part": nil,
					"source_name_part": nil,
				},
			},
		)
		if err != nil {
			return nil, err
		}
		linkInput, err := objFile.AsSourceFile()
		if err != nil {
			return nil, err
		}
		linkInputs = append(linkInputs, linkInput)
	}

	libs, err := collectLibs(ctx)
	if err != nil {
		return nil, err
	}

	// Maintain a set of link inputs, alongside the actual list of inputs.
	// We are about to start collecting necessary link inputs from dependencies,
	// and should skip already-added link inputs from the deps.
	// (Do not replace with a map to track link inputs, because the link order
	// needs to be maintained and Go maps have randomized key iteration.)
	seenLinkInputs := make(map[string]bool)
	for _, in := range linkInputs {
		seenLinkInputs[in.Filename()] = true
	}

	for dep, err := range ctx.ResolvedTargetsFor("deps") {
		if err != nil {
			return nil, err
		}
		if ccInfo, ok := dep.Metadata.(CxxInfo); ok {
			for _, lib := range ccInfo.LibraryFiles {
				libSrc, err := lib.AsSourceFile()
				if err != nil {
					return nil, err
				}
				if !seenLinkInputs[libSrc.Filename()] {
					linkInputs = append(linkInputs, libSrc)
					seenLinkInputs[libSrc.Filename()] = true
				}
			}
		}
		for _, depOutput := range dep.Metadata.Outputs() {
			switch path.Ext(depOutput.Path()) {
			case ".a", ".so":
				linkInput, err := depOutput.AsSourceFile()
				if err != nil {
					return nil, err
				}
				if !seenLinkInputs[linkInput.Filename()] {
					linkInputs = append(linkInputs, linkInput)
					seenLinkInputs[linkInput.Filename()] = true
				}
			default:
				// TODO: check for other dep input types.
				return nil, NotImplementedError{
					what: fmt.Sprintf("%q dep not implemented yet", depOutput.Path()),
				}
			}
		}
	}

	out, err := ctx.DeclareTool(
		"link",
		fs.SourceFile{},
		linkInputs,
		name,
		&graph.SimpleExpansions{Elems: map[string][]string{
			// TODO: fill these out.
			"ldflags":      ctx.ConfigValues.Ldflags,
			"libs":         formatLibExpansions(libs),
			"frameworks":   nil,
			"swiftmodules": nil,
		}},
	)
	if err != nil {
		return nil, err
	}

	return DefaultMetadata{[]fs.OutputPath{out}}, nil
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
	var cLibs []fs.SourceFile

	// The list of -Ldependency=<path> and -Clink-arg=<path> strings
	// needed to compile this target.
	var depFlags []string

	for dep, err := range ctx.ResolvedTargetsFor("deps") {
		if err != nil {
			return nil, err
		}
		if rustLib, ok := dep.Metadata.(RustLibraryMetadata); ok {
			src, err := rustLib.OutputRlib.AsSourceFile()
			if err != nil {
				return nil, err
			}
			transitiveRlibs = append(transitiveRlibs, src)
			transitiveRlibs = append(transitiveRlibs, rustLib.TransitiveRlibs...)
			depCrateName := rustLib.CrateName
			if alias, ok := aliasedDeps[dep.Label]; ok {
				depCrateName = alias
			}
			// TODO: maybe it's not filename? see test files for why this seems wrong.
			externs = append(externs, "--extern", fmt.Sprintf("%s=%s", depCrateName, rustLib.OutputRlib.Path()))
		}
		if ccInfo, ok := dep.Metadata.(CxxInfo); ok {
			for _, lib := range ccInfo.LibraryFiles {
				libSrc, err := lib.AsSourceFile()
				if err != nil {
					return nil, err
				}
				cLibs = append(cLibs, libSrc)
				depFlags = append(depFlags, fmt.Sprintf("-Clink-arg=%s", lib.Path()))
			}
		}
	}

	allInputs := append(rsInputs, transitiveRlibs...)
	allInputs = append(allInputs, cLibs...)
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
		&graph.SimpleExpansions{Elems: map[string][]string{
			"crate_name": {crateName},
			"crate_type": {crateType},
			"externs":    externs,
			"rustflags":  ctx.ConfigValues.Rustflags,
			"rustdeps":   depFlags,
		}},
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
	return DefaultMetadata{[]fs.OutputPath{out}}, nil
}
