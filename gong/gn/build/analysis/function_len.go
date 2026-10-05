// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"fmt"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type lenFunction struct{}

func (lenFunction) HelpShort() string {
	return "len: Returns the length of a string or a list."
}

func (lenFunction) Help() string {
	return `len: Returns the length of a string or a list.

  len(item)

  The argument can be a string or a list.

Examples:

  len("foo")  # 3
  len([ "a", "b", "c" ])  # 3
`
}

func (lenFunction) IsTarget() bool { return false }
func (lenFunction) Run(_ *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value) (resolve.Value, error) {
	if len(args) != 1 {
		return nil, resolve.ArgumentCountError{
			Call: call,
			Msg:  "Expecting exactly one argument.",
		}
	}

	switch v := args[0].(type) {
	case *resolve.StringValue:
		return resolve.NewOriginlessIntegerValue(int64(len(v.RawGNString()))), nil
	case *resolve.ListValue:
		return resolve.NewOriginlessIntegerValue(int64(v.Len())), nil
	default:
		return nil, resolve.TypeError{
			Value: v,
			Msg:   "len() expects a string or a list.",
			Help:  fmt.Sprintf("Got %s instead.", resolve.DescribeType(v)),
		}
	}
}
