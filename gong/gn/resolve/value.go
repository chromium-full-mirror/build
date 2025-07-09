// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"fmt"

	"go.starlark.net/starlark"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

type ValueType int

const (
	ValueTypeNone ValueType = iota
	ValueTypeBoolean
	ValueTypeInteger
	ValueTypeString
	ValueTypeList
	ValueTypeScope
)

func (t ValueType) String() string {
	switch t {
	case ValueTypeNone:
		return "none"
	case ValueTypeBoolean:
		return "boolean"
	case ValueTypeInteger:
		return "integer"
	case ValueTypeString:
		return "string"
	case ValueTypeList:
		return "list"
	case ValueTypeScope:
		return "scope"
	default:
		return "UNKNOWN"
	}
}

// Value represents a variable value in the interpreter.
type Value interface {
	starlark.Value
	// valueType is a convenience method to allow this package to
	// check a value's type without casting to a concrete type.
	valueType() ValueType
	// setOrigin is a convenience method to allow this package to set
	// the origin of a value without casting to a concrete type.
	setOrigin(parse.ParseNode)
	// OriginNode returns the origin parse node of the value.
	OriginNode() parse.ParseNode
	// CopyWithOrigin performs a shallow copy of the value with a new origin.
	CopyWithOrigin(parse.ParseNode) Value
	// RawGNString returns a GN-like stringification of the value.
	//
	// Behaves similarly to `Value::ToString(false)` in C++ GN, however because
	// of implementation differences a [ListValue] may be self-referential,
	// which is not possible in C++ GN. This edge case will result in `[...]`
	// being output.
	//
	// Callers that desire an equivalent to `Value::ToString(true)` in C++ GN
	// should instead call [GNLiteralRvalue].
	//
	// For a Python/Starlark-like representation, call String() instead.
	RawGNString() string
	// Equal compares values. Only the "value" is compared, not the origin. Scope
	// values check only the contents of the current scope, and do not go to
	// parent scopes.
	Equal(other Value) bool
}

// GNLiteralRvalue renders the value contents as a GN literal rvalue.
// Strings render with escaped quotes.
//
// Behaves similarly to `Value::ToString(true)` in C++ GN, however because
// of implementation differences a [ListValue] may be self-referential,
// which is not possible in C++ GN. This edge case will result in `[...]`
// being output.
//
// Callers that desire an equivalent to `Value::ToString(false)` in C++ GN
// should instead call [RawGNString] on the value directly.
func GNLiteralRvalue(v Value) string {
	if str, ok := v.(*StringValue); ok {
		// Direct port of the C++ GN string quotation logic.
		// This includes iterating through char instead of runes.
		result := "\""
		hangingBackslash := false
		for i := range len(str.value) {
			ch := str.value[i]
			// If the last character was a literal backslash and the next
			// character could form a valid escape sequence, we need to insert
			// an extra backslash to prevent that.
			if hangingBackslash && (ch == '$' || ch == '"' || ch == '\\') {
				result += "\\"
			}
			// If the next character is a dollar sign or double quote, it needs
			// to be escaped; otherwise it can be printed as is.
			if ch == '$' || ch == '"' {
				result += "\\"
			}
			result += string(ch)
			hangingBackslash = ch == '\\'
		}
		// Again, we need to prevent the closing double quotes from becoming
		// an escape sequence.
		if hangingBackslash {
			result += "\\"
		}
		result += "\""
		return result
	}
	return v.RawGNString()
}

// MakeErrFromValue makes an error at the provided value.
func MakeErrFromValue(value Value, message, helpText string) error {
	return syntax.MakeErrorAt(
		value.OriginNode().LocationRange().Begin(),
		[]syntax.LocationRange{value.OriginNode().LocationRange()},
		message,
		helpText)
}

// VerifyValueTypeIs returns a user-facing error that references the parse node
// if the value isn't the expected type.
func VerifyValueTypeIs(v Value, t ValueType) error {
	if v.valueType() == t {
		return nil
	}
	return parse.MakeErrFromParseNode(v.OriginNode(),
		fmt.Sprintf("This is not a %s. Instead I see a %s = true",
			t.String(),
			v.valueType().String()), "")
}
