// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

// template represents the information associated with a template() call in GN,
// which includes a closure and the code to run when the template is invoked.
type template struct {
	closure    *resolve.Scope
	definition *parse.FunctionCallNode
}

func (t *template) HelpShort() string { return "<template>" }
func (t *template) Help() string      { return "A template rule defined in the build file." }
func (t *template) IsTarget() bool    { return false }
func (t *template) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value, block *parse.BlockNode) (resolve.Value, error) {
	if len(args) == 0 {
		return nil, resolve.ArgumentCountError{
			OriginFunction: resolve.OriginFunction{Call: call},
			Msg:            "Incorrect arguments.",
			Help:           "This function requires a single string argument.",
		}
	}
	targetName := args[0]

	// First run the invocation's block to evaluate variables to pass to the template.
	invocationScope := scope.NewNestedScope()
	invocationScope.SetValue("target_name", targetName, call)
	if _, err := resolve.ExecuteNode(block, invocationScope); err != nil {
		return nil, err
	}

	// Set up the scope to run the template and set the current directory for the
	// template (which scope context provider uses to base the target-related
	// variables target_gen_dir and target_out_dir on) to be that of the invoker.
	// This way, files don't have to be rebased and target_*_dir works the way
	// people expect (otherwise it's too easy to be putting generated files in the
	// gen dir corresponding to an imported file).
	templateScope := t.closure.NewNestedScope()
	if ctx, ok := templateScope.ExecContext().(*scopeContext); ok {
		if invocationCtx, ok := scope.ExecContext().(*scopeContext); ok {
			ctx.itemCollector = invocationCtx.itemCollector
			ctx.sourceDir = invocationCtx.sourceDir
		}
	}
	invokerValue := resolve.NewScopeValue(invocationScope)
	templateScope.SetValue("invoker", invokerValue, call)
	templateScope.SetValue("target_name", targetName, call)

	// Actually run the template code.
	if _, err := resolve.ExecuteNode(t.definition.Block, templateScope); err != nil {
		return nil, err
	}

	// Mark magic variables as used to prevent unused variable warnings.
	invocationScope.Value("target_name", true)
	templateScope.Value("invoker", true)
	templateScope.Value("target_name", true)

	// Check for unused variables in the invocation scope. This will find typos
	// of things the caller meant to pass to the template but the template didn't
	// read out.
	if err := invocationScope.CheckForUnusedVars(); err != nil {
		return nil, err
	}

	// Check for unused variables in the template itself.
	if err := templateScope.CheckForUnusedVars(); err != nil {
		return nil, err
	}

	return nil, nil
}
