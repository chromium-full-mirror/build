// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type filterExcludeFunction struct{}

func (filterExcludeFunction) HelpShort() string {
	return "filter_exclude: Remove values that match a set of patterns."
}

func (filterExcludeFunction) Help() string {
	return `filter_exclude: Remove values that match a set of patterns.

  filter_exclude(values, exclude_patterns)

  The argument values must be a list of strings.

  The argument exclude_patterns must be a list of file patterns (see
  "gn help file_pattern"). Any elements in values matching at least one
  of those patterns will be excluded.

Examples
  values = [ "foo.cc", "foo.h", "foo.proto" ]
  result = filter_exclude(values, [ "*.proto" ])
  # result will be [ "foo.cc", "foo.h" ]
`
}

func (filterExcludeFunction) IsTarget() bool { return false }
func (filterExcludeFunction) Run(_ *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value) (resolve.Value, error) {
	return runFilter(call, args, excludeFilter)
}

type filterIncludeFunction struct{}

func (filterIncludeFunction) HelpShort() string {
	return "filter_include: Remove values that do not match a set of patterns."
}

func (filterIncludeFunction) Help() string {
	return `filter_include: Remove values that do not match a set of patterns.

  filter_include(values, include_patterns)

  The argument values must be a list of strings.

  The argument include_patterns must be a list of file patterns (see
  "gn help file_pattern"). Only elements from values matching at least
  one of the pattern will be included.

Examples
  values = [ "foo.cc", "foo.h", "foo.proto" ]
  result = filter_include(values, [ "*.proto" ])
  # result will be [ "foo.proto" ]
`
}

func (filterIncludeFunction) IsTarget() bool { return false }
func (filterIncludeFunction) Run(_ *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value) (resolve.Value, error) {
	return runFilter(call, args, includeFilter)
}

type filterSelection int

const (
	excludeFilter filterSelection = iota
	includeFilter
)

func runFilter(call *parse.FunctionCallNode, args []resolve.Value, selection filterSelection) (resolve.Value, error) {
	if len(args) != 2 {
		return nil, resolve.ArgumentCountError{
			OriginFunction: resolve.OriginFunction{Call: call},
			Msg:            "Expecting exactly two arguments.",
		}
	}

	// Arg 1 of 2 is the list of values to filter.
	inputList, err := resolve.AsValue[*resolve.ListValue](args[0])
	if err != nil {
		return nil, resolve.TypeError{
			Value: args[0],
			// Error message matching C++ GN.
			Msg: "First argument must be a list of strings.",
		}
	}

	// Arg 2 of 2 is the list of patterns to filter by.
	lv, err := resolve.AsValue[*resolve.ListValue](args[1])
	if err != nil {
		return nil, resolve.TypeError{
			Value: args[1],
			// Error message matching C++ GN.
			Msg: "This value must be a list.",
		}
	}
	patterns := make([]Pattern, 0, lv.Len())
	for value := range lv.Values() {
		sv, err := resolve.AsValue[*resolve.StringValue](value)
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, MakePattern(sv.RawGNString()))
	}

	var filteredResult []resolve.Value
	for value := range inputList.Values() {
		sv, err := resolve.AsValue[*resolve.StringValue](value)
		if err != nil {
			return nil, resolve.TypeError{
				Value: args[0],
				// Error message matching C++ GN.
				Msg: "First argument must be a list of strings.",
			}
		}

		matchesPattern := false
		for _, pattern := range patterns {
			if pattern.MatchString(sv.RawGNString()) {
				matchesPattern = true
				break
			}
		}

		switch selection {
		case includeFilter:
			if matchesPattern {
				filteredResult = append(filteredResult, value)
			}
		case excludeFilter:
			if !matchesPattern {
				filteredResult = append(filteredResult, value)
			}
		}
	}
	return resolve.NewOriginlessListValue(filteredResult), nil
}
