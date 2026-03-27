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
	// HACK: right now gong only has a concept of "tool" or "script" actions.
	// targets that should be written as phony right now are:
	//	- group(), which has 0 actions
	//	- anything with script calls only
	// so, we'll naively assume that if the target doesn't use tools, write it as phony.
	if slices.ContainsFunc(t.Resolution.Actions, func(action graph.Action) bool {
		_, ok := action.(graph.RunToolAction)
		return ok
	}) {
		targetLabel := t.Label()
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
		return nil
	}

	return writePhonyForTarget(w, t, buildSettings)
}

func writePhonyForTarget(w io.Writer, t *graph.Target, buildSettings *environment.BuildSettings) error {
	for _, action := range t.Resolution.Actions {
		switch action := action.(type) {
		case graph.RunScriptAction:
			writeAction(w, t, action, buildSettings)
		}
	}

	var outputPaths []string
	for _, output := range t.Resolution.Metadata.Outputs() {
		outputPaths = append(outputPaths, escapeStringNinja(output.Path()))
	}
	_, err := fmt.Fprintf(w, "build phony/%s: phony %s", escapeStringNinja(t.Label().Name), strings.Join(outputPaths, " "))
	if err != nil {
		return err
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
			return fmt.Errorf("script actions not implemented for binary targets")
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
		if err := writeAction(w, t, action, buildSettings); err != nil {
			return err
		}
	}
	return nil
}

// TODO: look into simplifying the fmt.Fprint + repeated err checking boilerplate.
// stylistically it makes it not as easy to review as well.
// would combining into larger format strings using backticks work?
// what performance impacts might happen as a result?
func writeAction(w io.Writer, t *graph.Target, action graph.Action, buildSettings *environment.BuildSettings) error {
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
		targetLabel := t.Label().UserVisibleString(true)
		ruleName := scriptRuleNormalizer.Replace(targetLabel) + "_rule"
		rebasedScript, err := fs.RebasePath(action.Script.Filename(), buildSettings.BuildDir, buildSettings.RootPath)
		if err != nil {
			return err
		}

		_, err = fmt.Fprintf(w, "rule %s\n", ruleName)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "  command = %s %s", escapeStringNinja(buildSettings.PythonPath), escapeStringNinja(rebasedScript))
		if err != nil {
			return err
		}
		for _, arg := range action.Args {
			_, err = fmt.Fprintf(w, " %s", escapeStringNinja(arg))
			if err != nil {
				return err
			}
		}
		_, err = fmt.Fprintln(w)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "  description = ACTION %s\n", targetLabel)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, "  restat = 1")
		if err != nil {
			return err
		}
		if action.Depfile != "" {
			_, err = fmt.Fprintf(w, "  depfile = %s\n", escapeStringNinja(action.Depfile))
			if err != nil {
				return err
			}
			// Using "deps = gcc" allows Ninja to read and store the depfile content in
			// its internal database which improves performance, especially for large
			// depfiles. The use of this feature with depfiles that contain multiple
			// outputs require Ninja version 1.9.0 or newer.
			// TODO: buildSettings needs to store ninja required version
			_, err = fmt.Fprintln(w, "  deps = gcc")
			if err != nil {
				return err
			}
		}
		_, err = fmt.Fprintln(w)
		if err != nil {
			return err
		}

		var outs []string
		for _, out := range action.Outputs {
			outs = append(outs, escapeStringNinja(out.Path()))
		}
		ins := []string{escapeStringNinja(rebasedScript)}
		for _, in := range action.Inputs {
			rebasedIn, err := fs.RebasePath(in.Filename(), buildSettings.BuildDir, buildSettings.RootPath)
			if err != nil {
				return err
			}
			ins = append(ins, escapeStringNinja(rebasedIn))
		}

		_, err = fmt.Fprintf(w, "build %s: %s | %s", strings.Join(outs, " "), ruleName, strings.Join(ins, " "))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown action type: %T", action)
	}
	return nil
}
