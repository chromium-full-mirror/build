// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"fmt"
	"io"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

// Tool represents arguments to a toolchain tool.
type Tool struct {
	Name        string
	Command     substitutionPattern
	outputs     []string // Simplified: List of output pattern strings
	Description substitutionPattern
	definedFrom parse.Node
}

// NewTool creates a new tool struct with the given name.
func NewTool(name string) *Tool {
	return &Tool{
		Name: name,
	}
}

// WriteNinjaRule writes the tool rule to the given writer.
// This is a rudimentary implementation.
// TODO: Use text/template? Need to escape ninja meta characters?
func (t *Tool) WriteNinjaRule(w io.Writer) error {
	_, err := fmt.Fprintf(w, "rule %s\n", t.Name)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "  command = ")
	if err != nil {
		return err
	}
	for _, sub := range t.Command.Pattern {
		_, err = w.Write([]byte(sub.NinjaString()))
		if err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(w)
	if err != nil {
		return err
	}

	if len(t.Description.Pattern) > 0 {
		_, err = fmt.Fprintf(w, "  description = ")
		if err != nil {
			return err
		}
		for _, sub := range t.Description.Pattern {
			_, err = w.Write([]byte(sub.NinjaString()))
			if err != nil {
				return err
			}
		}
		_, err = fmt.Fprintln(w)
		if err != nil {
			return err
		}
	}

	_, err = fmt.Fprintln(w)
	if err != nil {
		return err
	}
	return nil
}

// toolExecContext is used for execution inside a tool() call.
type toolExecContext struct {
	baseContext resolve.ExecContext
	toolchain   *Toolchain
}

func (t toolExecContext) BaseConfig() *resolve.Scope {
	return t.baseContext.BaseConfig()
}
func (t toolExecContext) NestedContext() resolve.ExecContext {
	return toolExecContext{
		baseContext: t,
		toolchain:   t.toolchain,
	}
}

// ToolFunction defines the tool() function.
// It is for exclusive use inside the toolchain() function, and will return
// an error if it is executed anywhere else.
type ToolFunction struct{}

func (ToolFunction) HelpShort() string { return "tool: Specify arguments to a toolchain tool." }
func (ToolFunction) Help() string      { return "" }
func (ToolFunction) IsTarget() bool    { return false }

func (ToolFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value, block *parse.BlockNode) (resolve.Value, error) {
	// Find the toolchain definition we're executing inside of.
	toolchainContext, ok := scope.ExecContext().(toolExecContext)
	if !ok {
		return nil, ToolOutsideToolchain{}
	}
	toolchain := toolchainContext.toolchain

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
	name := nameValue.RawGNString()

	blockScope := scope.NewNestedScopeWithContext(scope.ExecContext().NestedContext())
	if _, err := resolve.ExecuteNode(block, blockScope); err != nil {
		return nil, err
	}

	// TODO: verify tool name is valid for this toolchain.
	tool := NewTool(name)
	tool.definedFrom = call

	// Command required unless 'action' tool.
	v := blockScope.Value("command", true)
	wantCommand := name != "action"
	gotCommand := v != nil
	if gotCommand != wantCommand {
		err := ToolError{
			OriginNode: parse.OriginNode{Node: tool.definedFrom},
			message:    "This tool's command is bad.",
		}
		if !wantCommand {
			err.helpText = `This tool doesn't support "command".`
		} else {
			err.helpText = `This tool requires "command" to be defined.`
		}
		return nil, err
	}
	if gotCommand {
		sv, err := resolve.AsValue[*resolve.StringValue](v)
		if err != nil {
			return nil, err
		}
		tool.Command, err = makeSubstitutionPattern(sv.RawGNString())
		if err != nil {
			return nil, err
		}
	}

	// Outputs.
	// For now, just assume simple list.
	if v := blockScope.Value("outputs", true); v != nil {
		lv, err := resolve.AsValue[*resolve.ListValue](v)
		if err != nil {
			return nil, err
		}
		for item := range lv.Values() {
			sv, err := resolve.AsValue[*resolve.StringValue](item)
			if err != nil {
				return nil, err
			}
			tool.outputs = append(tool.outputs, sv.RawGNString())
		}
	}

	// Description (optional).
	if v := blockScope.Value("description", true); v != nil {
		sv, err := resolve.AsValue[*resolve.StringValue](v)
		if err != nil {
			return nil, err
		}
		tool.Description, err = makeSubstitutionPattern(sv.RawGNString())
		if err != nil {
			return nil, err
		}
	}

	// Values that haven't been implemented yet.
	// TODO: Use these values.
	blockScope.Value("default_output_dir", true)
	blockScope.Value("default_output_extension", true)
	blockScope.Value("depend_output", true)
	blockScope.Value("depsformat", true)
	blockScope.Value("link_output", true)
	blockScope.Value("output_prefix", true)
	blockScope.Value("rspfile_content", true)

	toolchain.Tools[name] = tool
	return nil, blockScope.CheckForUnusedVars()
}
