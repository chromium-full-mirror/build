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

// Config is an item in the GN dependency graph that represents a named config.
//
// A config can list other configs. We track both the data assigned directly
// on the config, this list of sub-configs, and (when the config is resolved)
// the resulting values of everything merged together. The flatten step
// means we can avoid doing a recursive config walk for every target to compute
// flags.
type Config struct {
	ItemInfo
}

// CompatibleWith checks whether the other item is also a *Config.
func (Config) CompatibleWith(item Item) bool {
	switch item.(type) {
	case *Config:
		return true
	}
	return false
}

// NewConfig generates a config item.
func NewConfig(dir fs.SourceDir, scope *resolve.Scope, toolchain environment.Label, call *parse.FunctionCallNode, nameValue *resolve.StringValue, block *parse.BlockNode) (*Config, error) {
	name := nameValue.RawGNString()
	label := environment.Label{
		Dir:           dir,
		Name:          name,
		ToolchainDir:  toolchain.Dir,
		ToolchainName: toolchain.Name,
	}

	blockScope := scope.NewNestedScope()
	if _, err := resolve.ExecuteNode(block, blockScope); err != nil {
		return nil, err
	}

	// TODO: stub impl, need to store the values (cflags, etc)
	cfg := &Config{
		ItemInfo: ItemInfo{
			label:       label,
			definedFrom: call,
		},
	}

	err := blockScope.CheckForUnusedVars()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}
