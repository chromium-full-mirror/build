// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"fmt"
	"io"
	"strings"

	"go.chromium.org/build/gong/gn/build/graph"
)

// WriteTarget is a rudimentary stub implementation of writing a ninja build target out.
func WriteTarget(w io.Writer, t *graph.Target) error {
	for _, action := range t.Resolution.Actions {
		var inputPaths []string
		var implicitDeps []string
		if !action.Source.IsZero() {
			inputPaths = []string{"FAKEPATH" + action.Source.Filename()}
			for _, in := range action.Inputs {
				if in == action.Source {
					continue
				}
				implicitDeps = append(implicitDeps, "FAKEPATH"+in.Filename())
			}
		} else {
			for _, in := range action.Inputs {
				inputPaths = append(inputPaths, "FAKEPATH"+in.Filename())
			}
		}

		// TODO: escape special chars (e.g. space, $, : etc?)
		_, err := fmt.Fprintf(w, "build %s: %s %s",
			"FAKEPATH"+action.Output.Filename(),
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

		_, err = fmt.Fprint(w, "\n")
		if err != nil {
			return err
		}
	}
	return nil
}
