// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"fmt"

	"go.chromium.org/build/gong/gn/build/graph"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type targetFunction struct {
	schema *graph.Schema
}

func (targetFunction) IsTarget() bool { return true }
func (f targetFunction) HelpShort() string {
	return fmt.Sprintf("%s: %s", f.schema.Name, f.schema.Summary)
}
func (f targetFunction) Help() string { return f.HelpShort() } // TODO: support full description
func (f targetFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value, block *parse.BlockNode) (resolve.Value, error) {
	ctx, err := contextFromScope(scope)
	if err != nil {
		return nil, err
	}
	if ctx.isProcessingBuildConfig() {
		return nil, ItemInBuildConfigError{Call: call}
	}

	if len(args) == 0 {
		return nil, resolve.ArgumentCountError{
			Call: call,
			Msg:  "Target name is missing.",
		}
	}
	nameValue, err := resolve.AsValue[*resolve.StringValue](args[0])
	if err != nil {
		return nil, err
	}

	target, err := f.schema.Generate(ctx.sourceDir, scope, ctx.settings.toolchainLabel, call, nameValue, block)
	if err != nil {
		return nil, err
	}

	ctx.itemCollector(target)
	return nil, nil
}

type toolchainFunction struct{}

func (toolchainFunction) IsTarget() bool    { return false }
func (toolchainFunction) HelpShort() string { return "toolchain: Defines a toolchain." }
func (toolchainFunction) Help() string      { return "" }
func (f toolchainFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value, block *parse.BlockNode) (resolve.Value, error) {
	ctx, err := contextFromScope(scope)
	if err != nil {
		return nil, err
	}
	if ctx.isProcessingBuildConfig() {
		return nil, ItemInBuildConfigError{Call: call}
	}

	if len(args) == 0 {
		return nil, resolve.ArgumentCountError{
			Call: call,
			Msg:  "Incorrect arguments.",
			Help: "This function requires a single string argument.",
		}
	}
	nameValue, err := resolve.AsValue[*resolve.StringValue](args[0])
	if err != nil {
		return nil, err
	}

	toolchain, err := graph.NewToolchain(ctx.sourceDir, scope, call, nameValue, block)
	if err != nil {
		return nil, err
	}

	ctx.itemCollector(toolchain)
	return nil, nil
}
