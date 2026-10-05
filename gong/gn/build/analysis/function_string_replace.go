// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"math"
	"strings"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type stringReplaceFunction struct{}

func (stringReplaceFunction) HelpShort() string {
	return "string_replace: Replaces substring in the given string."
}

func (stringReplaceFunction) Help() string {
	return `string_replace: Replaces substring in the given string.

  result = string_replace(str, old, new[, max])

  Returns a copy of the string str in which the occurrences of old have been
  replaced with new, optionally restricting the number of replacements. The
  replacement is performed sequentially, so if new contains old, it won't be
  replaced.

Example

  The code:
    mystr = "Hello, world!"
    print(string_replace(mystr, "world", "GN"))

  Will print:
    Hello, GN!
`
}

func (stringReplaceFunction) IsTarget() bool { return false }

func (stringReplaceFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value) (resolve.Value, error) {
	if len(args) < 3 || len(args) > 4 {
		return nil, resolve.ArgumentCountError{
			Call: call,
			Msg:  "Wrong number of arguments to string_replace().",
			Help: "Usage: string_replace(str, old, new[, max])",
		}
	}

	strVal, err := resolve.AsValue[*resolve.StringValue](args[0])
	if err != nil {
		return nil, err
	}
	str := strVal.RawGNString()

	oldVal, err := resolve.AsValue[*resolve.StringValue](args[1])
	if err != nil {
		return nil, err
	}
	old := oldVal.RawGNString()

	newVal, err := resolve.AsValue[*resolve.StringValue](args[2])
	if err != nil {
		return nil, err
	}
	new := newVal.RawGNString()

	max := int64(-1)
	if len(args) > 3 {
		iv, err := resolve.AsValue[*resolve.IntegerValue](args[3])
		if err != nil {
			return nil, err
		}
		max = iv.Value()
		if max <= 0 {
			return nil, resolve.ValueError{
				Call: call,
				Msg:  "Requested number of replacements is not positive.",
				Help: "Usage: string_replace(str, old, new[, max])",
			}
		}
	}

	// For 32-bit platforms, int64 max value is larger than int max value.
	for max > math.MaxInt {
		str = strings.Replace(str, old, new, math.MaxInt)
		max -= math.MaxInt
	}

	str = strings.Replace(str, old, new, int(max))
	return resolve.NewOriginlessStringValue(str), nil
}
