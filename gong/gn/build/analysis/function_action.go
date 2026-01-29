// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
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
		// TODO: make a helper function for this - all targets should output this exact same error message if called from build config.
		return nil, call.Function.MakeErrorWithHelp(
			syntax.ErrInvalidOperation,
			"Not valid from the build config.",
			`You can't do this kind of thing from the build config script, silly!
Put it in a regular BUILD file.`,
		)
	}

	return nil, call.Function.MakeError(syntax.ErrNotImplemented, "action not yet implemented")
}
