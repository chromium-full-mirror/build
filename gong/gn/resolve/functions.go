// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

// FunctionInfo represents a simple function that only takes arguments and returns a Value.
// TODO: Implement support for functions with blocks (i.e. targets) etc.
type FunctionInfo interface {
	// HelpShort returns a short help string.
	HelpShort() string
	// Help returns an extended help string.
	Help() string
	// IsTarget returns whether this function represents a target.
	IsTarget() bool
	// Run executes the function.
	Run(scope *Scope, call *parse.FunctionCallNode, args []Value) (Value, error)
}

// AssertFunction is a function that asserts an expression is true.
type AssertFunction struct{}

func (AssertFunction) HelpShort() string {
	return "assert: Assert an expression is true at generation time."
}
func (AssertFunction) Help() string {
	return `assert(<condition> [, <error string>])

  If the condition is false, the build will fail with an error. If the
  optional second argument is provided, that string will be printed
  with the error message.`
}
func (AssertFunction) IsTarget() bool { return false }
func (AssertFunction) Run(scope *Scope, call *parse.FunctionCallNode, args []Value) (Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, call.Function.MakeErrorWithHelp(syntax.ErrArgumentCount,
			"Wrong number of arguments for assert.",
			"assert() takes one or two arguments, were you expecting something else?")
	}

	assertValue, err := AsValue[*BooleanValue](args[0])
	if err != nil {
		return nil, call.Function.MakeError(syntax.ErrTypeMismatch, "Assertion value not a bool.")
	}
	assertMessage := ""
	if len(args) == 2 {
		assertMessageValue, err := AsValue[*StringValue](args[1])
		if err != nil {
			return nil, call.Function.MakeError(syntax.ErrTypeMismatch, "Assertion message is not a string.")
		}
		assertMessage = assertMessageValue.value
	}

	if !assertValue.value {
		// TODO: use args[0].origin to add extra hint "this is where it was set"
		return nil, call.Function.MakeErrorWithHelp(syntax.ErrInvalidOperation,
			"Assertion failed.",
			assertMessage)
	}
	return nil, nil
}

// mockFunction is a mock implementation of FunctionInfo for testing.
type mockFunction struct {
	value int64
}

func (mockFunction) HelpShort() string { return "mock function" }
func (mockFunction) Help() string      { return "A mock function for testing that adds 42 to input." }
func (mockFunction) IsTarget() bool    { return false }
func (f *mockFunction) Run(scope *Scope, call *parse.FunctionCallNode, args []Value) (Value, error) {
	if len(args) != 1 {
		return nil, call.Function.MakeError(syntax.ErrArgumentCount, "Expected 1 argument")
	}
	intVal, ok := args[0].(*IntegerValue)
	if !ok {
		return nil, MakeErrFromValue(args[0], syntax.ErrTypeMismatch, "Expected an integer", "Please provide an integer argument")
	}
	return &IntegerValue{value: intVal.value + 42, origin: call}, nil
}
