// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type configFunction struct{}

func (configFunction) HelpShort() string { return "config: Defines a configuration object." }
func (configFunction) Help() string      { return "" }
func (configFunction) IsTarget() bool    { return false }

func (configFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value, block *parse.BlockNode) (resolve.Value, error) {
	ctx, err := contextFromScope(scope)
	if err != nil {
		return nil, err
	}
	if ctx.isProcessingBuildConfig() {
		return nil, ItemInBuildConfigError{OriginFunction: resolve.OriginFunction{Call: call}}
	}

	if len(args) != 1 {
		return nil, resolve.ArgumentCountError{
			OriginFunction: resolve.OriginFunction{Call: call},
			Msg:            "Incorrect arguments.",
		}
	}
	nameValue, err := resolve.AsValue[*resolve.StringValue](args[0])
	if err != nil {
		return nil, err
	}
	name := nameValue.RawGNString()

	label := environment.Label{Dir: ctx.sourceDir, Name: name}

	blockScope := scope.NewNestedScope()
	if _, err := resolve.ExecuteNode(block, blockScope); err != nil {
		return nil, err
	}

	// TODO: stub impl, need to store the values (cflags, etc)
	cfg := &Config{
		itemInfo: itemInfo{
			label:       label,
			definedFrom: call,
		},
	}

	ctx.itemCollector(cfg)
	return nil, blockScope.CheckForUnusedVars()
}
