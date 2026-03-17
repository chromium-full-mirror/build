// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package ninjawriter writes ninja files for a build invocation.
package ninjawriter

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

// Write writes all ninja files to the build dir of the provided build invocation.
//
// Unlike C++ GN's NinjaWriter::RunAndWriteFiles, this takes the actual target objects
// rather than ninja rules strings for each target in order to simplify implementation.
// We may want to follow C++ GN and instead pass ninja rule strings, *if* it turns out
// that doing the same optimization of preparing each target's ninja rule (and subninja
// if necessary) asynchronously is worthwhile.
//
// TODO: use io/fs to test expected file outputs?
func Write(toolchains map[environment.Label]*graph.Toolchain, targetsByToolchain map[environment.Label][]*graph.Target, buildSettings *environment.BuildSettings) error {
	// HACK: the default toolchain is not exposed from the analysis package right now,
	// and requires further thought to decide how to expose it.
	// for now, always pretend the first lexographic toolchain is the default toolchain,
	// and throw a very visible warning that this assumption is being made
	// so that people aren't caught out by this unexpected behavior.
	toolchainsSorted := slices.Collect(maps.Keys(toolchains))
	if len(toolchainsSorted) == 0 {
		return fmt.Errorf("no toolchains")
	}
	slices.SortFunc(toolchainsSorted, func(a, b environment.Label) int {
		return a.Compare(b)
	})
	defaultToolchain := toolchainsSorted[0]
	if len(toolchainsSorted) > 1 {
		defaultToolchain = toolchainsSorted[0]
		fmt.Fprintf(os.Stderr, "WARNING: default toolchain detection is NOT IMPLEMENTED!\n")
		fmt.Fprintf(os.Stderr, "WARNING: arbitrarily choosing %s as default!\n", defaultToolchain.UserVisibleString(false))
		fmt.Fprintf(os.Stderr, "WARNING: this is probably not what you want!\n")
	}

	// Write each toolchain.ninja file.
	var tcNinjasRel []string
	for _, tcLabel := range toolchainsSorted {
		targets, ok := targetsByToolchain[tcLabel]
		if !ok {
			return fmt.Errorf("couldn't find targets for toolchain %s", tcLabel.UserVisibleString(false))
		}
		tc, ok := toolchains[tcLabel]
		if !ok {
			return fmt.Errorf("couldn't find toolchain %s", tcLabel.UserVisibleString(false))
		}

		var tcDir fs.SourceDir
		var err error
		if tcLabel == defaultToolchain {
			tcDir = buildSettings.BuildDir
		} else {
			// For now just assume the toolchain name is always a valid dir name. We may
			// want to clean up the in the future.
			// https://source.chromium.org/gn/gn/+/main:src/gn/filesystem_utils.cc;l=963-965;drc=4526cdec9338674dfcc2a4b87cfe4b3231d046a9
			tcDir, err = buildSettings.BuildDir.ResolveRelativeDir(tcLabel.Name)
			if err != nil {
				return fmt.Errorf("failed to determine toolchain %s outdir: %w", tcLabel.UserVisibleString(false), err)
			}
		}

		tcNinjaFile, err := tcDir.ResolveRelativeFile("toolchain.ninja")
		if err != nil {
			return fmt.Errorf("failed to determine target %s ninjafile: %w", tcLabel.UserVisibleString(false), err)
		}
		tcNinjaRel, err := fs.RebasePath(tcNinjaFile.Filename(), buildSettings.BuildDir, buildSettings.RootPath)
		if err != nil {
			return fmt.Errorf("failed to determine target %s ninjafile relpath: %w", tcLabel.UserVisibleString(false), err)
		}
		tcNinjasRel = append(tcNinjasRel, tcNinjaRel)

		outDirAbs := buildSettings.FullDirPath(tcDir)
		if err := os.MkdirAll(outDirAbs, 0755); err != nil {
			return fmt.Errorf("failed to create toolchain dir: %w", err)
		}

		tcFileAbs := buildSettings.FullPath(tcNinjaFile)
		tf, err := os.Create(tcFileAbs)
		if err != nil {
			return fmt.Errorf("failed to create toolchain file %s: %w", tcFileAbs, err)
		}

		// Prepare ninja rules for each target.
		var rules []string
		for _, target := range targets {
			var sb strings.Builder
			if err := writeTarget(&sb, target, buildSettings); err != nil {
				if err := tf.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "failed to close %s: %v", tcFileAbs, err)
				}
				return fmt.Errorf("write target failed: %w", err)
			}
			rules = append(rules, sb.String())
		}

		if err := writeToolchain(tf, tc, rules); err != nil {
			if err := tf.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "failed to close %s: %v", tcFileAbs, err)
			}
			return fmt.Errorf("failed to write toolchain ninja: %w", err)
		}

		if err := tf.Close(); err != nil {
			return err
		}
	}

	// Unconditionally write the build.ninja. Ninja's build-out-of-date
	// checking will re-run GN when any build input is newer than build.ninja, so
	// any time the build is updated, build.ninja's timestamp needs to updated
	// also, even if the contents haven't been changed.
	buildNinjaFile, err := buildSettings.BuildDir.ResolveRelativeFile("build.ninja")
	if err != nil {
		return fmt.Errorf("failed to determine root ninjafile: %w", err)
	}
	buildNinjaAbs := buildSettings.FullPath(buildNinjaFile)
	bf, err := os.Create(buildNinjaAbs)
	if err != nil {
		return fmt.Errorf("failed to create root ninjafile: %w", err)
	}
	defer func() {
		cerr := bf.Close()
		if err == nil {
			err = cerr
		}
	}()

	// TODO: check for err when printing
	fmt.Fprint(bf, "# TODO: ninja_required_version\n")
	fmt.Fprint(bf, "# TODO: rule gn\n")
	fmt.Fprint(bf, "# TODO: rule build.ninja.stamp\n")
	fmt.Fprint(bf, "# TODO: rule build.ninja\n")
	for _, tcNinjaRel := range tcNinjasRel {
		fmt.Fprintf(bf, "subninja %s\n", tcNinjaRel)
	}

	if defaultTargets, ok := targetsByToolchain[defaultToolchain]; ok {
		if err := writePhonyAndAllRules(bf, defaultTargets, buildSettings); err != nil {
			return err
		}
	}

	return err
}
