// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"strings"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type stringSplitFunction struct{}

func (stringSplitFunction) HelpShort() string {
	return "string_split: Split string into a list of strings."
}

func (stringSplitFunction) Help() string {
	return `string_split: Split string into a list of strings.

  result = string_split(str[, sep])

  Split string into all substrings separated by separator and returns a list
  of the substrings between those separators.

  If the separator argument is omitted, the split is by any whitespace, and
  any leading/trailing whitespace is ignored; similar to Python's str.split().

Examples without a separator (split on whitespace):

  string_split("")          --> []
  string_split("a")         --> ["a"]
  string_split(" aa  bb")   --> ["aa", "bb"]

Examples with a separator (split on separators):

  string_split("", "|")           --> [""]
  string_split("  a b  ", " ")    --> ["", "", "a", "b", "", ""]
  string_split("aa+-bb+-c", "+-") --> ["aa", "bb", "c"]
`
}

func (stringSplitFunction) IsTarget() bool { return false }
func (stringSplitFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value) (resolve.Value, error) {
	// Check usage: argument count.
	if len(args) != 1 && len(args) != 2 {
		return nil, resolve.ArgumentCountError{
			OriginFunction: resolve.OriginFunction{Call: call},
			Msg:            "Wrong number of arguments to string_split().",
			Help:           "Usage: string_split(str[, sep])",
		}
	}

	// Check usage: str is a string.
	strVal, err := resolve.AsValue[*resolve.StringValue](args[0])
	if err != nil {
		return nil, err
	}
	str := strVal.RawGNString()

	// Check usage: sep is a non-empty string.
	var sep string
	if len(args) == 2 {
		sepVal, err := resolve.AsValue[*resolve.StringValue](args[1])
		if err != nil {
			return nil, err
		}
		sep = sepVal.RawGNString()
		if sep == "" {
			return nil, resolve.ValueError{
				OriginFunction: resolve.OriginFunction{Call: call},
				Msg:            "Separator argument to string_split() cannot be empty string",
				Help:           "Usage: string_split(str[, sep])",
			}
		}
	}

	// Split the string.
	var split []string
	if sep != "" {
		// Case: Explicit separator argument.
		split = strings.Split(str, sep)
	} else {
		// Case: Split on any whitespace and strip ends.
		split = strings.Fields(str)
	}

	// Convert slice of strings to GN list of GN strings.
	resultValues := make([]resolve.Value, len(split))
	for i, s := range split {
		resultValues[i] = resolve.NewOriginlessStringValue(s)
	}
	return resolve.NewOriginlessListValue(resultValues), nil
}
