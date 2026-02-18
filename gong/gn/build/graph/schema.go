// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"fmt"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

// A Schema is the type definition of a GN target.
//
// The word "schema" is an implementation detail; we're calling them "schemas" to avoid
// overloading the word "type" across both GN and Go contexts.
//
// User-facing documentation should still refer to GN target "types".
//
// The schema of a target specifies a name (e.g. "shared_library"), the types of variables
// it accepts (e.g. "sources", "deps"), and how it resolves a target definition.
type Schema struct {
	Name     string
	Summary  string
	Vars     map[string]VarType // TODO: Implement concept of required?
	Resolver ResolverFn
}

// Generate creates a target from this schema.
func (s *Schema) Generate(dir fs.SourceDir, scope *resolve.Scope, toolchain environment.Label, call *parse.FunctionCallNode, nameValue *resolve.StringValue, block *parse.BlockNode) (*Target, error) {
	if block == nil {
		return nil, fmt.Errorf("target definition missing block?")
	}

	blockScope := scope.NewNestedScope()
	if _, err := resolve.ExecuteNode(block, blockScope); err != nil {
		return nil, err
	}

	name := nameValue.RawGNString()
	label := environment.Label{
		Dir:           dir,
		Name:          name,
		ToolchainDir:  toolchain.Dir,
		ToolchainName: toolchain.Name,
	}

	target := &Target{
		ItemInfo: ItemInfo{
			label:       label,
			definedFrom: call,
		},
		Schema: s,
		Values: map[string]ProcessedValue{
			"name": StringValue{
				origin: nameValue,
				str:    name,
			},
		},
	}

	// Validate all of the target's values and perform initial processing.
	for acceptedVar, expectedType := range s.Vars {
		value := blockScope.Value(acceptedVar, true)
		if value == nil {
			continue
		}
		processedValue, err := target.processValue(value, expectedType)
		if err != nil {
			return nil, err
		}
		target.Values[acceptedVar] = processedValue
	}

	err := blockScope.CheckForUnusedVars()
	if err != nil {
		return nil, err
	}
	return target, nil
}
