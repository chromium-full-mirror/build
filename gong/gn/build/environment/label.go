// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package environment

import (
	"strings"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

// Label represents the name of a target or some other named thing in
// the source path. The label is always absolute and always includes a name
// part, so it starts with a slash, and has one colon.
type Label struct {
	Dir           fs.SourceDir
	Name          string
	ToolchainDir  fs.SourceDir
	ToolchainName string
}

// ResolveLabel computes a string from a build file that may be relative to the
// current directory into a fully qualified label.
func ResolveLabel(currentDir fs.SourceDir, currentToolchain Label, input resolve.Value) (Label, error) {
	stringValue, ok := input.(*resolve.StringValue)
	if !ok {
		return Label{}, resolve.TypeError{
			Value: input,
			Msg:   "Dependency is not a string.",
		}
	}
	str := stringValue.RawGNString()
	if str == "" {
		return Label{}, resolve.MakeErrFromValue(input, syntax.ErrInvalidFormat, "Dependency string is empty.", "")
	}

	loc, labelName, inputToolchain, err := splitLabelComponents(str, input, false)
	if err != nil {
		return Label{}, err
	}
	if loc == "" {
		return Label{}, resolve.MakeErrFromValue(input, syntax.ErrNotImplemented, "Implicit target location not yet supported.", "")
	}
	if labelName == "" {
		return Label{}, resolve.MakeErrFromValue(input, syntax.ErrNotImplemented, "Implicit target name not yet supported.", "")
	}

	// For now, naively derive the label directory from the location.
	// This means implicit location isn't supported.
	// TODO: support implicit locations.
	labelDir, err := fs.MakeSourceDir(loc)
	if err != nil {
		return Label{}, err
	}

	// Use the current toolchain unless the input explicitly overrides it.
	toolchainName := currentToolchain.Name
	toolchainDir := currentToolchain.Dir
	if inputToolchain != "" {
		loc, toolchainName, _, err = splitLabelComponents(inputToolchain, input, true)
		if err != nil {
			return Label{}, err
		}
		if toolchainName == "" {
			return Label{}, resolve.MakeErrFromValue(input, syntax.ErrNotImplemented, "Implicit toolchain name not yet supported.", "")
		}
		// For now, naively derive the toolchain directory from the location.
		// This means implicit toolchain location isn't supported.
		// TODO: support implicit toolchain locations.
		toolchainDir, err = fs.MakeSourceDir(loc)
		if err != nil {
			return Label{}, err
		}
	}

	return Label{
		Dir:           labelDir,
		Name:          labelName,
		ToolchainDir:  toolchainDir,
		ToolchainName: toolchainName,
	}, nil
}

// splitLabelComponents splits input into "location", "name", and "toolchain".
// It performs basic validation and returns the parts.
//
// The label might be for e.g. a target that can have a toolchain,
// or it might be a toolchain label itself. The latter case can't have a toolchain.
//
// Examples:
//
//	"//foo/bar:baz(tc)" -> loc="//foo/bar", name="baz", tc="tc"
//	"//foo/bar:baz"     -> loc="//foo/bar", name="baz", tc=""
//	"//foo/bar"         -> loc="//foo/bar", name="",    tc=""
//	"//foo/bar(tc)"     -> loc="//foo/bar", name="bar", tc="tc"
//	":baz"              -> loc="",          name="baz", tc=""
//	":baz(tc)"          -> loc="",          name="baz", tc="tc"
func splitLabelComponents(str string, origin resolve.Value, isToolchain bool) (loc, name, tc string, err error) {
	// This function is going to be constantly used to parse labels in buildfiles.
	// Rather than immediately using strings.Cut on both ':' and '(', scan for either of the two.
	// Then depending on the first one of those that's found, parse the rest of the string.
	loc = str
	for i, c := range str {
		// Found a ( first.
		// This means a label with a location but no name e.g.
		//	"//foo/bar(tc)"
		// It can't be anything else e.g.
		//	":baz(tc)"
		// would be caught by the colon case first.
		if c == '(' {
			loc = str[:i]
			tc = str[i+1:]
			break
		}
		// Found a colon.
		// Everything before it is the location, everything after it is the name and (optional) toolchain.
		if c == ':' {
			loc = str[:i]
			name, tc, _ = strings.Cut(str[i+1:], "(")
			break
		}
	}

	if tc != "" {
		// The toolchain was pulled out with the trailing ')' still attached.
		// Remove it now, and ensure we aren't parsing a nested toolchain.
		if isToolchain {
			return "", "", "", resolve.MakeErrFromValue(origin, syntax.ErrInvalidFormat,
				"Toolchain has a toolchain.",
				`Your toolchain definition (inside the parens) seems to itself have a
toolchain. Don't do this.`)
		}
		if tc[len(tc)-1] != ')' {
			return "", "", "", resolve.MakeErrFromValue(origin, syntax.ErrInvalidFormat,
				"Bad toolchain name.",
				`Toolchain name must end in a ")" at the end of the label.`)
		}
		tc = tc[:len(tc)-1]
	}

	// We allow three cases:
	//   Absolute:                "//foo:bar" -> /foo:bar
	//   Target in current file:  ":foo"     -> <currentdir>:foo
	//   Path with implicit name: "/foo"     -> /foo:foo
	if loc == "" && name == "" {
		// Can't use both implicit filename and name (":").
		return "", "", "", resolve.MakeErrFromValue(origin, syntax.ErrInvalidFormat,
			"This doesn't specify a dependency.", "")
	}

	return loc, name, tc, nil
}

// UserVisibleString formats this label in a way that we can present to the user
// or expose to other parts of the system. Source directories end in slashes,
// but the user expects names like "//chrome/renderer:renderer_config" when
// printed. The toolchain is optionally included.
func (l Label) UserVisibleString(includeToolchain bool) string {
	var sb strings.Builder
	sb.Grow(len(l.Dir.Path()) + len(l.Name) + 1)
	if l.Dir.Empty() {
		return ""
	}
	sb.WriteString(l.Dir.WithNoTrailingSlash())
	sb.WriteRune(':')
	sb.WriteString(l.Name)
	if includeToolchain {
		sb.WriteRune('(')
		if !l.ToolchainDir.Empty() && l.ToolchainName != "" {
			sb.WriteString(l.ToolchainDir.WithNoTrailingSlash())
			sb.WriteRune(':')
			sb.WriteString(l.ToolchainName)
		}
		sb.WriteRune(')')
	}
	return sb.String()
}
