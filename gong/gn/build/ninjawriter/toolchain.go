// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package ninjawriter writes ninja files for build targets.
package ninjawriter

import (
	"io"
	"slices"

	"go.chromium.org/build/gong/gn/build/graph"
)

// WriteToolchain is a rudimentary stub implementation of writing a ninja toolchain out.
func WriteToolchain(w io.Writer, tc *graph.Toolchain) error {
	var names []string
	for name := range tc.Tools {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		err := tc.Tools[name].WriteNinjaRule(w)
		if err != nil {
			return err
		}
	}
	return nil
}
