// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"errors"
	"fmt"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/ui"
)

// FunctionInfo represents common metadata for a function.
type FunctionInfo interface {
	// HelpShort returns a short help string.
	HelpShort() string
	// Help returns an extended help string.
	Help() string
	// IsTarget returns whether this function represents a target.
	IsTarget() bool
}

// BlockFunctionInfo represents a function that takes arguments and a block and returns a Value.
// The Run function may assume that block is never nil.
type BlockFunctionInfo interface {
	FunctionInfo
	// Run executes the function.
	Run(scope *Scope, call *parse.FunctionCallNode, args []Value, block *parse.BlockNode) (Value, error)
}

// SimpleFunctionInfo represents a simple function that only takes arguments and returns a Value.
type SimpleFunctionInfo interface {
	FunctionInfo
	// Run executes the function.
	Run(scope *Scope, call *parse.FunctionCallNode, args []Value) (Value, error)
}

// EnsureSingleStringArg is a helper to check for a single string arg,
// and standardize the error message if this isn't the case.
func EnsureSingleStringArg(function *parse.FunctionCallNode, args []Value) (*StringValue, error) {
	if len(args) != 1 {
		return nil, ArgumentCountError{
			OriginFunction: OriginFunction{Call: function},
			Msg:            "Incorrect arguments.",
			Help:           "This function requires a single string argument.",
		}
	}
	return AsValue[*StringValue](args[0])
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
		return nil, ArgumentCountError{
			OriginFunction: OriginFunction{call},
			Msg:            "Wrong number of arguments for assert.",
			Help:           "assert() takes one or two arguments, were you expecting something else?",
		}
	}

	assertValue, err := AsValue[*BooleanValue](args[0])
	if err != nil {
		return nil, TypeError{
			Value: args[0],
			Msg:   "Assertion value not a bool.",
		}
	}
	assertMessage := ""
	if len(args) == 2 {
		assertMessageValue, err := AsValue[*StringValue](args[1])
		if err != nil {
			return nil, TypeError{
				Value: args[1],
				Msg:   "Assertion message is not a string.",
			}
		}
		assertMessage = assertMessageValue.value
	}

	if !assertValue.value {
		// TODO: use args[0].origin to add extra hint "this is where it was set"
		return nil, AssertError{
			OriginFunction: OriginFunction{Call: call},
			Details:        assertMessage,
		}
	}
	return nil, nil
}

// AssertFailureFunction is an internal function for testing that checks a block fails with the given error message.
type AssertFailureFunction struct{}

func (AssertFailureFunction) HelpShort() string { return "assert_failure: Internal function." }
func (AssertFailureFunction) Help() string {
	return `assert_failure(<error string> [, <help string>]) {
  <block that should fail>
}

  Executes the provided block, expecting it to fail with the provided
  error string. If the optional help string is specified, also checks
  that the help string matches.

  Only GN errors are expected. Any other Go error type is treated as an
  internal failure.

Examples

  assert_failure("Assertion failed.") {
	assert(false)
  }

  assert_failure("Negative array subscript.", "You gave me -1") {
    a = b[-1]
  }
`
}
func (AssertFailureFunction) IsTarget() bool { return false }
func (AssertFailureFunction) Run(scope *Scope, call *parse.FunctionCallNode, args []Value, block *parse.BlockNode) (Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, ArgumentCountError{
			OriginFunction: OriginFunction{Call: call},
			Msg:            "Wrong number of arguments for assert_failure.",
			Help:           fmt.Sprintf("assert_failure() takes one or two arguments, but %d were given.", len(args))}
	}

	_, err := ExecuteNode(block, scope)
	if gnErr, ok := errors.AsType[ui.PresentableError](err); ok {
		assertMessageValue, err := AsValue[*StringValue](args[0])
		if err != nil {
			return nil, TypeError{
				Value: args[0],
				Msg:   "Assertion message is not a string.",
			}
		}
		assertMessage := assertMessageValue.value
		if gnErr.Message() != assertMessage {
			return nil, AssertError{
				OriginFunction: OriginFunction{Call: call},
				Details:        fmt.Sprintf("Wanted %q, got %q", assertMessage, gnErr.Message()),
			}
		}

		if len(args) == 2 {
			helpMessageValue, err := AsValue[*StringValue](args[1])
			if err != nil {
				return nil, TypeError{
					Value: args[1],
					Msg:   "Help message is not a string.",
				}
			}
			helpMessage := helpMessageValue.value
			if gnErr.HelpText() != helpMessage {
				return nil, AssertError{
					OriginFunction: OriginFunction{Call: call},
					Details:        fmt.Sprintf("Wanted %q, got %q", helpMessage, gnErr.HelpText()),
				}
			}
		}
		return nil, nil
	}
	if err != nil {
		return nil, AssertError{
			OriginFunction: OriginFunction{Call: call},
			Details:        fmt.Sprintf("Non-GN error encountered during execution of this block: %v", err),
		}
	}
	return nil, AssertError{
		OriginFunction: OriginFunction{Call: call},
		Details:        "Block did not fail.",
	}
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
		return nil, ArgumentCountError{
			OriginFunction: OriginFunction{Call: call},
			Msg:            "Expected 1 argument",
		}
	}
	intVal, ok := args[0].(*IntegerValue)
	if !ok {
		return nil, TypeError{
			Value: args[0],
			Msg:   "Expected an integer",
			Help:  "Please provide an integer argument",
		}
	}
	return &IntegerValue{value: intVal.value + 42, origin: call}, nil
}
