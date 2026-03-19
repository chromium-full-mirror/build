// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
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
			outputPaths = append(outputPaths, escapeStringNinja(output.Path()))
		}
		_, err := fmt.Fprintf(w, "build phony/%s: phony %s", escapeStringNinja(targetLabel.Name), strings.Join(outputPaths, " "))
		if err != nil {
			return err
		}
	} else {
		// TODO: reusing C++ GN's builddir resolution funcs is really clumsy.
		// can this be improved by adopting io/fs and its FS and SubFS interfaces?
		// alternatively, look more carefully at how C++ GN uses the funcs
		// for example do we want GetBuildDirForTargetAsSourceDir, etc?
		// https://source.chromium.org/gn/gn/+/main:src/gn/filesystem_utils.cc;l=1097;drc=4526cdec9338674dfcc2a4b87cfe4b3231d046a9
		targetDir := t.OutDir(buildSettings)
		targetNinjaRel := path.Join(targetDir.Path(), fmt.Sprintf("%s.ninja", targetLabel.Name))
		targetNinjaOutput := fs.MakeOutputPath(buildSettings.BuildDir, targetNinjaRel)

		targetDirAsSource, err := targetDir.AsSourceDir()
		if err != nil {
			return fmt.Errorf("failed to determine target %s outdir: %w", targetLabel.UserVisibleString(true), err)
		}
		targetDirAbs := buildSettings.FullDirPath(targetDirAsSource)
		if err := os.MkdirAll(targetDirAbs, 0755); err != nil {
			return fmt.Errorf("failed to create target dir: %w", err)
		}

		targetNinjaFile, err := targetNinjaOutput.AsSourceFile()
		if err != nil {
			return fmt.Errorf("failed to determine target %s ninjafile: %w", targetLabel.UserVisibleString(true), err)
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

		_, err = fmt.Fprintf(w, "subninja %s", escapeStringNinja(targetNinjaRel))
		if err != nil {
			return err
		}
	}

	return nil
}

func writeBinaryTarget(w io.Writer, t *graph.Target, buildSettings *environment.BuildSettings) error {
	var outputExtension, targetOutputName string
	targetOutDir := t.OutDir(buildSettings)
	outputDir := targetOutDir.Path()

	// HACK: Decide the "final output" for this target.
	// In C++ GN, this is done by Tool::GetToolTypeForTargetFinalOutput, which checks the actual
	// type of the target.
	// This is not trivial to implement in this codebase currently, so for now naively assume
	// the final tool declared by the target is the "final output".
	if len(t.Resolution.Actions) > 0 {
		lastAction := t.Resolution.Actions[len(t.Resolution.Actions)-1]
		switch action := lastAction.(type) {
		case graph.RunToolAction:
			outputBase := filepath.Base(action.Output.Path())
			outputExtension = filepath.Ext(outputBase)
			targetOutputName = strings.TrimSuffix(outputBase, outputExtension)
		case graph.RunScriptAction:
			return fmt.Errorf("script actions not implemented yet")
		default:
			return fmt.Errorf("unknown action type: %T", action)
		}
	} else {
		targetOutputName = t.Label().Name
	}

	// Read target declaration for top-level overrides if set.
	if nameVar, err := t.StringFor("output_name"); err == nil {
		targetOutputName = nameVar
	}
	if extVar, err := t.StringFor("output_extension"); err == nil {
		if extVar != "" {
			outputExtension = "." + extVar
		} else {
			outputExtension = ""
		}
	}
	if dirVar, err := t.StringFor("output_dir"); err == nil {
		outputDir = dirVar
	}

	// Now write the substitutions that depend on the target and
	// do not vary on a per-file basis.
	var err error
	if outputExtension != "" {
		_, err = fmt.Fprintf(w, "output_extension = %s\n", escapeStringNinja(outputExtension))
		if err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "output_dir = %s\n", escapeStringNinja(fs.DirectoryWithNoLastSlash(outputDir)))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "target_output_name = %s\n", escapeStringNinja(targetOutputName))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "target_out_dir = %s\n", escapeStringNinja(fs.DirectoryWithNoLastSlash(targetOutDir.Path())))
	if err != nil {
		return err
	}

	// End of target-level substitutions.
	_, err = fmt.Fprintln(w)
	if err != nil {
		return err
	}

	// Then, write out each action.
	for _, action := range t.Resolution.Actions {
		if err := writeAction(w, action, buildSettings); err != nil {
			return err
		}
	}
	return nil
}

func writeAction(w io.Writer, action graph.Action, buildSettings *environment.BuildSettings) error {
	switch action := action.(type) {
	case graph.RunToolAction:
		var inputPaths []string
		var implicitDeps []string
		if !action.Source.IsZero() {
			rebasedSource, err := fs.RebasePath(action.Source.Filename(), buildSettings.BuildDir, buildSettings.RootPath)
			if err != nil {
				return err
			}
			inputPaths = []string{escapeStringNinja(rebasedSource)}
			for _, in := range action.Inputs {
				if in == action.Source {
					continue
				}
				rebasedIn, err := fs.RebasePath(in.Filename(), buildSettings.BuildDir, buildSettings.RootPath)
				if err != nil {
					return err
				}
				implicitDeps = append(implicitDeps, escapeStringNinja(rebasedIn))
			}
		} else {
			for _, in := range action.Inputs {
				rebasedIn, err := fs.RebasePath(in.Filename(), buildSettings.BuildDir, buildSettings.RootPath)
				if err != nil {
					return err
				}
				inputPaths = append(inputPaths, escapeStringNinja(rebasedIn))
			}
		}

		rebasedOutput := escapeStringNinja(action.Output.Path())

		_, err := fmt.Fprintf(w, "build %s: %s %s",
			rebasedOutput,
			escapeStringNinja(action.Tool),
			strings.Join(inputPaths, " "),
		)
		if err != nil {
			return err
		}

		if len(implicitDeps) > 0 {
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
			_, err := fmt.Fprintf(w, "  %s =", k)
			if err != nil {
				return err
			}
			v := action.Expansions[k]
			if v != "" {
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
	case graph.RunScriptAction:
		return fmt.Errorf("script actions not implemented yet")
	default:
		return fmt.Errorf("unknown action type: %T", action)
	}
	return nil
}
