// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

// writeTarget is a rudimentary stub implementation of writing a ninja build target out.
//
// It is nowhere near "correct" if the definition is "replicate C++ GN's outputs",
// but is enough to help gong in its current incarnation write working Ninja for
// very basic build repos.
//
// (To resolve this, one thing we need is for Builder to stop hardcoding target outdirs.
// Instead some of that logic will likely need to move to this package. After all, where
// outputs should go can be thought of as an implementation detail of ninjawriter.)
//
// TODO: use io/fs to test expected file outputs?
func writeTarget(w io.Writer, t *graph.Target, buildSettings *environment.BuildSettings) error {
	targetLabel := t.Label()

	// TODO: this is a hack that naively assumes all targets are either phony or binary.
	// obviously this is not correct and is only going to work for very simple builds.
	if len(t.Resolution.Actions) == 0 {
		var outputPaths []string
		for _, output := range t.Resolution.Metadata.Outputs() {
			// TODO: need to port OutputFile so that output path can easily be obtained from SourceFile?
			outputRel, err := fs.RebasePath(output.Filename(), buildSettings.BuildDir, buildSettings.RootPath)
			if err != nil {
				return fmt.Errorf("failed to determine input %s outpath: %w", targetLabel.UserVisibleString(true), err)
			}
			outputPaths = append(outputPaths, outputRel)
		}
		_, err := fmt.Fprintf(w, "build phony/%s: phony %s", targetLabel.Name, strings.Join(outputPaths, " "))
		if err != nil {
			return err
		}
	} else {
		// TODO: reusing C++ GN's builddir resolution funcs is somewhat clumsy.
		// can this be improved by adopting io/fs and its FS and SubFS interfaces?
		targetDir, err := t.OutDir(buildSettings)
		if err != nil {
			return fmt.Errorf("failed to determine target %s outdir: %w", targetLabel.UserVisibleString(true), err)
		}
		targetNinjaFile, err := targetDir.ResolveRelativeFile(fmt.Sprintf("%s.ninja", targetLabel.Name))
		if err != nil {
			return fmt.Errorf("failed to determine target %s ninjafile: %w", targetLabel.UserVisibleString(true), err)
		}

		outDirAbs := buildSettings.FullDirPath(targetDir)
		if err := os.MkdirAll(outDirAbs, 0755); err != nil {
			return fmt.Errorf("failed to create target dir: %w", err)
		}

		targetNinjaAbs := buildSettings.FullPath(targetNinjaFile)
		subninjaFile, err := os.Create(targetNinjaAbs)
		if err != nil {
			return fmt.Errorf("failed to create target %s: %w", targetNinjaAbs, err)
		}
		if err := writeBinaryTarget(subninjaFile, t, buildSettings); err != nil {
			if err := subninjaFile.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "failed to close %s: %v", targetNinjaAbs, err)
			}
			return fmt.Errorf("failed to write target %s: %w", targetLabel.UserVisibleString(true), err)
		}
		if err := subninjaFile.Close(); err != nil {
			return err
		}

		// TODO: need to port OutputFile so that output path can easily be obtained from SourceFile?
		targetNinjaRel, err := fs.RebasePath(targetNinjaFile.Filename(), buildSettings.BuildDir, buildSettings.RootPath)
		if err != nil {
			return fmt.Errorf("failed to determine target %s relpath: %w", targetLabel.UserVisibleString(true), err)
		}
		_, err = fmt.Fprintf(w, "subninja %s", targetNinjaRel)
		if err != nil {
			return err
		}
	}

	return nil
}

func writeBinaryTarget(w io.Writer, t *graph.Target, buildSettings *environment.BuildSettings) error {
	targetOutDir, err := t.OutDir(buildSettings)
	if err != nil {
		return err
	}
	targetOutBuildDirRel, err := fs.RebasePath(targetOutDir.WithNoTrailingSlash(), buildSettings.BuildDir, buildSettings.RootPath)
	if err != nil {
		return err
	}
	// TODO: escape special chars (e.g. space, $, : etc?)
	_, err = fmt.Fprintf(w, "target_out_dir = %s\n", targetOutBuildDirRel)
	if err != nil {
		return err
	}
	// TODO: more top-level substitutions
	_, err = fmt.Fprintln(w)
	if err != nil {
		return err
	}

	for _, action := range t.Resolution.Actions {
		var inputPaths []string
		var implicitDeps []string
		if !action.Source.IsZero() {
			rebasedSource, err := fs.RebasePath(action.Source.Filename(), buildSettings.BuildDir, buildSettings.RootPath)
			if err != nil {
				return err
			}
			inputPaths = []string{rebasedSource}
			for _, in := range action.Inputs {
				if in == action.Source {
					continue
				}
				rebasedIn, err := fs.RebasePath(in.Filename(), buildSettings.BuildDir, buildSettings.RootPath)
				if err != nil {
					return err
				}
				implicitDeps = append(implicitDeps, rebasedIn)
			}
		} else {
			for _, in := range action.Inputs {
				rebasedIn, err := fs.RebasePath(in.Filename(), buildSettings.BuildDir, buildSettings.RootPath)
				if err != nil {
					return err
				}
				inputPaths = append(inputPaths, rebasedIn)
			}
		}

		rebasedOutput, err := fs.RebasePath(action.Output.Filename(), buildSettings.BuildDir, buildSettings.RootPath)
		if err != nil {
			return err
		}

		// TODO: escape special chars (e.g. space, $, : etc?)
		_, err = fmt.Fprintf(w, "build %s: %s %s",
			rebasedOutput,
			action.Tool,
			strings.Join(inputPaths, " "),
		)
		if err != nil {
			return err
		}

		if len(implicitDeps) > 0 {
			// TODO: likewise escape?
			_, err = fmt.Fprint(w, " | "+strings.Join(implicitDeps, " "))
			if err != nil {
				return err
			}
		}

		_, err = fmt.Fprintln(w)
		if err != nil {
			return err
		}

		for _, k := range slices.Sorted(maps.Keys(action.Expansions)) {
			// TODO: likewise escape?
			_, err := fmt.Fprintf(w, "  %s =", k)
			if err != nil {
				return err
			}
			v := action.Expansions[k]
			if v != "" {
				// TODO: likewise escape?
				_, err = fmt.Fprintf(w, " %s", v)
				if err != nil {
					return err
				}
			}
			_, err = fmt.Fprintln(w)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
