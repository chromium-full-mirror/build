// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"fmt"
	"io"
	"slices"

	"go.chromium.org/build/gong/gn/build/graph"
)

// writeToolchain is a rudimentary stub implementation of writing a ninja toolchain out.
func writeToolchain(w io.Writer, tc *graph.Toolchain, rules []string) error {
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

	for _, rule := range rules {
		_, err := fmt.Fprintln(w, rule)
		if err != nil {
			return err
		}
	}
	return nil
}
