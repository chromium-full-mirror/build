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
		for _, in := range action.Inputs {
			inputPaths = append(inputPaths, "FAKEPATH"+in.Filename())
		}

		_, err := fmt.Fprintf(w, "build %s: %s %s\n",
			"FAKEPATH"+action.Output.Filename(),
			action.Tool,
			strings.Join(inputPaths, " "),
		)
		if err != nil {
			return err
		}
	}
	return nil
}
