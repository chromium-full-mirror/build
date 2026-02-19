// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

// Toolchain is an item in the GN dependency graph that represents information
// on a specific toolchain. This data is filled in when we encounter a toolchain
// definition.
//
// This class is an Item so it can participate in dependency management. In
// particular, when a target uses a toolchain, it should have a dependency on
// that toolchain's object so that we can be sure we loaded the toolchain
// before generating the build for that target.
type Toolchain struct {
	ItemInfo
	// Tools defined in this toolchain.
	Tools map[string]*Tool
}

// NewToolchain creates a new toolchain from a toolchain definition.
// It executes the toolchain's block in a nested scope and collects the tools defined in it.
func NewToolchain(dir fs.SourceDir, scope *resolve.Scope, call *parse.FunctionCallNode, nameValue *resolve.StringValue, block *parse.BlockNode) (*Toolchain, error) {
	name := nameValue.RawGNString()

	// Note that we don't want to make a label that includes the toolchain name
	// in the label, since toolchain labels don't themselves have toolchain names.
	label := environment.Label{Dir: dir, Name: name}

	toolchain := &Toolchain{
		ItemInfo: ItemInfo{
			label: label,
		},
		Tools: make(map[string]*Tool),
	}
	toolchain.definedFrom = call

	// Scope for executing the toolchain's block.
	blockScope := scope.NewNestedScopeWithContext(toolExecContext{
		baseContext: scope.ExecContext().NestedContext(),
		toolchain:   toolchain,
	})

	// TODO: buildDependencyFiles needs to be collected from the scope.
	if _, err := resolve.ExecuteNode(block, blockScope); err != nil {
		return nil, err
	}

	err := blockScope.CheckForUnusedVars()
	if err != nil {
		return nil, err
	}
	return toolchain, nil
}

func (Toolchain) CompatibleWith(item Item) bool {
	switch item.(type) {
	case *Toolchain:
		return true
	}
	return false
}
