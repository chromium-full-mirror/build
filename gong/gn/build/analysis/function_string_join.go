// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"strings"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type stringJoinFunction struct{}

func (stringJoinFunction) HelpShort() string {
	return "string_join: Concatenates a list of strings with a separator."
}

func (stringJoinFunction) Help() string {
	return `string_join: Concatenates a list of strings with a separator.

  result = string_join(separator, strings)

  Concatenate a list of strings with intervening occurrences of separator.

Examples

    string_join("", ["a", "b", "c"])    --> "abc"
    string_join("|", ["a", "b", "c"])   --> "a|b|c"
    string_join(", ", ["a", "b", "c"])  --> "a, b, c"
    string_join("s", ["", ""])          --> "s"
`
}

func (stringJoinFunction) IsTarget() bool { return false }

func (stringJoinFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value) (resolve.Value, error) {
	// Check usage: Number of arguments.
	if len(args) != 2 {
		return nil, resolve.ArgumentCountError{
			Call: call,
			Msg:  "Wrong number of arguments to string_join().",
			Help: "Expecting exactly two. usage: string_join(separator, strings)",
		}
	}

	// Check usage: separator is a string.
	separatorVal, err := resolve.AsValue[*resolve.StringValue](args[0])
	if err != nil {
		return nil, resolve.TypeError{
			Value: args[0],
			Msg:   "separator in string_join(separator, strings) is not a string",
			Help:  "Expecting separator argument to be a string.",
		}
	}
	separator := separatorVal.RawGNString()

	// Check usage: strings is a list.
	listVal, err := resolve.AsValue[*resolve.ListValue](args[1])
	if err != nil {
		return nil, resolve.TypeError{
			Value: args[1],
			Msg:   "strings in string_join(separator, strings) is not a list",
			Help:  "Expecting strings argument to be a list.",
		}
	}

	// Arguments look good; do the join.
	// TODO: can potentially avoid duplicating the slice with strings.Builder?
	stringsToJoin := make([]string, 0, listVal.Len())
	for argVal := range listVal.Values() {
		strVal, err := resolve.AsValue[*resolve.StringValue](argVal)
		if err != nil {
			return nil, err
		}
		stringsToJoin = append(stringsToJoin, strVal.RawGNString())
	}

	joined := strings.Join(stringsToJoin, separator)
	return resolve.NewStringValueAt(call, joined), nil
}
