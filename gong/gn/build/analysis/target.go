// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
)

// Target represents a node in the GN build graph.
// It starts in an unresolved state, and is resolved by the Builder.
//
// In Bazel terms, this can be thought of as a node in the "configured target graph"
// after it has been resolved.
//
// Unlike Bazel, GN does not build an explicit pre-configuration plain target graph.
//
//nolint:unused
type Target struct {
	settings    *Settings
	label       environment.Label
	definedFrom parse.Node
	// TODO: if targets own "actions", then outputs should live under the "action graph".
	outputs []fs.OutputFile
}
