// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package graph represents items in the GN dependency graph.
package graph

import (
	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/parse"
)

// An Item is a named node in the GN dependency graph.
type Item interface {
	// CompatibleWith checks whether the other item has the same type.
	// Only checks pointer types.
	CompatibleWith(Item) bool
	// Label returns the label of the item.
	Label() environment.Label
	// DefinedFrom returns the node that defined the item.
	DefinedFrom() parse.Node
}

// ItemInfo contains the common fields for all Items and implements the Item interface.
type ItemInfo struct {
	label       environment.Label
	definedFrom parse.Node
}

// Label implements the Item interface.
func (i ItemInfo) Label() environment.Label {
	return i.label
}

// DefinedFrom implements the Item interface.
func (i ItemInfo) DefinedFrom() parse.Node {
	return i.definedFrom
}
