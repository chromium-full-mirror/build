// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type actionFunction struct {
}

func (actionFunction) IsTarget() bool { return true }
func (actionFunction) HelpShort() string {
	return "action: Declare a target that runs a script a single time."
}
func (actionFunction) Help() string {
	return `action: Declare a target that runs a script a single time.

  This is a stub function and is not implemented.
`
}

func (actionFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, _ []resolve.Value) (resolve.Value, error) {
	ctx, err := contextFromScope(scope)
	if err != nil {
		return nil, err
	}

	if ctx.isProcessingBuildConfig() {
		return nil, ItemInBuildConfigError{OriginFunction: resolve.OriginFunction{Call: call}}
	}

	return nil, NotImplementedError{OriginFunction: resolve.OriginFunction{Call: call}, what: "action target"}
}
