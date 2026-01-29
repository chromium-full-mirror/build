// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type setDefaultsFunction struct {
}

func (setDefaultsFunction) IsTarget() bool { return false }
func (setDefaultsFunction) HelpShort() string {
	return "set_defaults: Set default values for a target type."
}
func (setDefaultsFunction) Help() string {
	return `set_defaults: Set default values for a target type.

  set_defaults(<target_type_name>) { <values...> }

  Sets the default values for a given target type. Whenever target_type_name is
  seen in the future, the values specified in set_default's block will be
  copied into the current scope.

  When the target type is used, the variable copying is very strict. If a
  variable with that name is already in scope, the build will fail with an
  error.

  set_defaults can be used for built-in target types ("executable",
  "shared_library", etc.) and custom ones defined via the "template" command.
  It can be called more than once and the most recent call in any scope will
  apply, but there is no way to refer to the previous defaults and modify them
  (each call to set_defaults must supply a complete list of all defaults it
  wants). If you want to share defaults, store them in a separate variable.

Example

  set_defaults("static_library") {
    configs = [ "//tools/mything:settings" ]
  }

  static_library("mylib") {
    # The configs will be auto-populated as above. You can remove it if
    # you don't want the default for a particular default:
    configs -= [ "//tools/mything:settings" ]
  }
`
}

func (f *setDefaultsFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value, block *parse.BlockNode) (resolve.Value, error) {
	ctx, err := contextFromScope(scope)
	if err != nil {
		return nil, err
	}

	targetTypeName, err := resolve.EnsureSingleStringArg(call, args)
	if err != nil {
		return nil, err
	}

	// Run the block for the rule invocation.
	blockScope := scope.NewNestedScope()
	_, err = resolve.ExecuteNode(block, blockScope)
	if err != nil {
		return nil, err
	}

	// Now copy the values set on the scope we made into the free-floating one
	// (with no containing scope) used to hold the target defaults.
	dest := ctx.settings.NewScope()
	err = blockScope.NewNestedScope().NonRecursiveMergeTo(dest, resolve.ScopeMergeOptions{
		SourceNode:         call,
		SourceFriendlyName: "set_defaults()",
	})
	if err != nil {
		return nil, err
	}

	ctx.targetDefaults[targetTypeName.RawGNString()] = dest
	return nil, nil
}
