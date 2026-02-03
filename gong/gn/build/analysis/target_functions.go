// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"fmt"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type executableFunction struct{}

func (executableFunction) HelpShort() string { return "executable: Declare an executable target." }
func (executableFunction) Help() string      { return "" }
func (executableFunction) IsTarget() bool    { return true }
func (executableFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value, block *parse.BlockNode) (resolve.Value, error) {
	return executeGenericTarget("executable", scope, call, args, block)
}

type sharedLibraryFunction struct{}

func (sharedLibraryFunction) HelpShort() string {
	return "shared_library: Declare a shared_library target."
}
func (sharedLibraryFunction) Help() string   { return "" }
func (sharedLibraryFunction) IsTarget() bool { return true }
func (sharedLibraryFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value, block *parse.BlockNode) (resolve.Value, error) {
	return executeGenericTarget("shared_library", scope, call, args, block)
}

type staticLibraryFunction struct{}

func (staticLibraryFunction) HelpShort() string {
	return "static_library: Declare a static_library target."
}
func (staticLibraryFunction) Help() string   { return "" }
func (staticLibraryFunction) IsTarget() bool { return true }
func (staticLibraryFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value, block *parse.BlockNode) (resolve.Value, error) {
	return executeGenericTarget("static_library", scope, call, args, block)
}

type copyFunction struct{}

func (copyFunction) HelpShort() string { return "copy: Declare a copy target." }
func (copyFunction) Help() string      { return "" }
func (copyFunction) IsTarget() bool    { return true }
func (copyFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value, block *parse.BlockNode) (resolve.Value, error) {
	return executeGenericTarget("copy", scope, call, args, block)
}

// Placeholder function to demonstrate that GN graph item collection works.
func executeGenericTarget(targetType string, scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value, block *parse.BlockNode) (resolve.Value, error) {
	ctx, err := contextFromScope(scope)
	if err != nil {
		return nil, err
	}

	if ctx.isProcessingBuildConfig() {
		return nil, ItemInBuildConfigError{OriginFunction: resolve.OriginFunction{Call: call}}
	}

	if block == nil {
		return nil, fmt.Errorf("target definition missing block?")
	}

	blockScope := scope.NewNestedScope()

	// Set target_name.
	if len(args) == 0 {
		return nil, resolve.ArgumentCountError{
			OriginFunction: resolve.OriginFunction{Call: call},
			Msg:            "Target name is missing.",
		}
	}
	nameValue, err := resolve.AsValue[*resolve.StringValue](args[0])
	if err != nil {
		return nil, err
	}

	blockScope.SetValue("target_name", nameValue, call)

	if _, err := resolve.ExecuteNode(block, blockScope); err != nil {
		return nil, err
	}

	label := environment.Label{
		Dir:  ctx.sourceDir,
		Name: nameValue.RawGNString(),
		// TODO: Toolchain
	}

	target := &Target{
		itemInfo: itemInfo{
			label:       label,
			definedFrom: call,
		},
		targetType: targetType,
	}

	// Fill dependencies here, then the builder will check to know what deps need to be loaded.
	if err := fillDependencies(target, ctx.sourceDir, blockScope); err != nil {
		return nil, err
	}

	// TODO: fill other data.

	ctx.itemCollector(target)

	return nil, nil
}

// fillDependencies populates the target's dependencies from the "deps" variable in the scope.
func fillDependencies(target *Target, sourceDir fs.SourceDir, scope *resolve.Scope) error {
	depsValue := scope.Value("deps", true)
	if depsValue == nil {
		return nil
	}

	listValue, err := resolve.AsValue[*resolve.ListValue](depsValue)
	if err != nil {
		return err
	}

	for value := range listValue.Values() {
		resolvedLabel, err := environment.ResolveLabel(sourceDir, environment.Label{}, value)
		if err != nil {
			return err
		}

		target.privateDeps = append(target.privateDeps, LabelTargetPair{
			Label:  resolvedLabel,
			Origin: value.OriginNode(),
		})
	}
	return nil
}
