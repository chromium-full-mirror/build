// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"go.chromium.org/build/gong/gn/build/graph"
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
			Help:           "This function requires a single string argument.",
		}
	}
	nameValue, err := resolve.AsValue[*resolve.StringValue](args[0])
	if err != nil {
		return nil, err
	}

	config, err := graph.NewConfig(ctx.sourceDir, scope, ctx.settings.toolchainLabel, call, nameValue, block)
	if err != nil {
		return nil, err
	}

	ctx.itemCollector(config)
	return nil, nil
}
