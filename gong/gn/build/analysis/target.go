// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
)

// Target is an item in the GN dependency graph that represents either an unresolved
// or resolved build target.
//
// A target starts in an unresolved state, and is resolved by the Builder.
//
// In Bazel terms, a resolved target can be thought of as a node in the
// "configured target graph".
//
// Unlike Bazel, GN does not build an "unconfigured" target graph.
//
//nolint:unused
type Target struct {
	itemInfo
	settings    *Settings
	targetType  string
	privateDeps []LabelTargetPair
	// TODO: if targets own "actions", then outputs should live under the "action graph".
	outputs []fs.OutputFile
}

func (Target) compatibleWith(item Item) bool {
	switch item.(type) {
	case *Target:
		return true
	}
	return false
}

// LabelTargetPair represents a label, and a pointer to its target if that
// dependency has been resolved.
type LabelTargetPair struct {
	// Label is the label of the dependency.
	Label environment.Label
	// Origin is the parse node where this dependency was defined.
	Origin parse.Node
	// Target is the resolved target. This may be nil if the target has not
	// been resolved yet.
	Target *Target
}
