// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"fmt"
	"strings"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type printFunction struct{}

func (printFunction) HelpShort() string {
	return "print: Prints to the console."
}

func (printFunction) Help() string {
	return `print: Prints to the console.

  Prints all arguments to the console separated by spaces. A newline is
  automatically appended to the end.

  This function is intended for debugging. Note that build files are run in
  parallel so you may get interleaved prints. A buildfile may also be executed
  more than once in parallel in the context of different toolchains so the
  prints from one file may be duplicated or
  interleaved with itself.

Examples

  print("Hello world")

  print(sources, deps)
`
}

func (printFunction) IsTarget() bool { return false }
func (printFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value) (resolve.Value, error) {
	ctx, err := contextFromScope(scope)
	if err != nil {
		return nil, err
	}

	var sb strings.Builder
	for i, arg := range args {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(arg.RawGNString())
	}
	sb.WriteByte('\n')
	msg := sb.String()

	if ctx.settings.buildSettings.PrintCallback != nil {
		ctx.settings.buildSettings.PrintCallback(msg)
	} else {
		fmt.Print(msg)
	}

	return nil, nil
}
