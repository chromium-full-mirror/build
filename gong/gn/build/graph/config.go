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

// ConfigVars define all variables a target or config item accept to set config values.
var ConfigVars = map[string]TargetVar{
	"arflags":         StringListVar{},
	"asmflags":        StringListVar{},
	"cflags":          StringListVar{},
	"cflags_c":        StringListVar{},
	"cflags_cc":       StringListVar{},
	"cflags_objc":     StringListVar{},
	"cflags_objcc":    StringListVar{},
	"defines":         StringListVar{},
	"frameworks":      StringListVar{},
	"weak_frameworks": StringListVar{},
	"inputs":          FileListVar{},
	"ldflags":         StringListVar{},
	"libs":            StringListVar{},
	"rustflags":       StringListVar{},
	"rustenv":         StringListVar{},
	"swiftflags":      StringListVar{},
	"configs":         LabelListVar{Expected: &Config{}},
}

// Config is an item in the GN dependency graph that represents a named config.
//
// A config can list other configs. We track both the data assigned directly
// on the config, this list of sub-configs, and (when the config is resolved)
// the resulting values of everything merged together. The flatten step
// means we can avoid doing a recursive config walk for every target to compute
// flags.
type Config struct {
	ItemInfo
	// DefinedValues are the processed values from the config item definition.
	DefinedValues map[string]ProcessedValue
	// resolvedValues are the final values after the config item is resolved.
	resolvedValues *ConfigValues
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

	cfg := &Config{
		label:         label,
		definedFrom:   call,
		DefinedValues: make(map[string]ProcessedValue),
	}

	for varName, varType := range ConfigVars {
		if val := blockScope.Value(varName, true); val != nil {
			processed, err := varType.Process(label, val)
			if err != nil {
				return nil, err
			}
			cfg.DefinedValues[varName] = processed
		}
	}

	err := blockScope.CheckForUnusedVars()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// Resolve computes the merged ConfigValues for this config and the provided deps.
// (The composite config is expanded to be the concatenation of its
// own values, and in order, the values from its sub-configs.)
func (c *Config) Resolve(configDeps []*Config) (*ConfigValues, error) {
	if c.resolvedValues != nil {
		return c.resolvedValues, nil
	}

	configValues, err := MakeConfigValues(c.DefinedValues)
	if err != nil {
		return nil, err
	}

	c.resolvedValues = &configValues
	if err := c.resolvedValues.Append(configDeps...); err != nil {
		return nil, err
	}

	return c.resolvedValues, nil
}
