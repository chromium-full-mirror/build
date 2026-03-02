// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
)

// WriteTarget is a rudimentary stub implementation of writing a ninja build target out.
//
// It is nowhere near "correct" if the definition is "replicate C++ GN's outputs",
// but is enough to help gong in its current incarnation write working Ninja files.
//
// (To resolve this, one thing we need is for Builder to stop hardcoding target outdirs.
// Instead some of that logic will likely need to move to this package. After all, where
// outputs should go can be thought of as an implementation detail of ninjawriter.)
func WriteTarget(w io.Writer, t *graph.Target, buildSettings *environment.BuildSettings) error {
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
