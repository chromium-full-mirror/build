// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package ninjawriter writes ninja files for build targets.
package ninjawriter

import (
	"fmt"
	"io"
	"slices"

	"go.chromium.org/build/gong/gn/build/analysis"
)

// WriteToolchain is a rudimentary stub implementation of writing a ninja toolchain out.
func WriteToolchain(w io.Writer, tc *analysis.Toolchain) error {
	var names []string
	for name := range tc.Tools {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		tool := tc.Tools[name]

		_, err := fmt.Fprintf(w, "rule %s\n", tool.Name)
		if err != nil {
			return err
		}

		_, err = fmt.Fprintf(w, "  command = %s\n", tool.Command)
		if err != nil {
			return err
		}

		if tool.Description != "" {
			_, err = fmt.Fprintf(w, "  description = %s\n", tool.Description)
			if err != nil {
				return err
			}
		}

		_, err = fmt.Fprintln(w)
		if err != nil {
			return err
		}
	}
	return nil
}
