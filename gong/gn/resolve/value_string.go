// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"go.starlark.net/starlark"
	starsyntax "go.starlark.net/syntax"

	"go.chromium.org/build/gong/gn/parse"
)

// StringValue represents a GN string.
type StringValue struct {
	origin parse.Node
	value  string
}

func NewOriginlessStringValue(value string) *StringValue {
	return &StringValue{value: value}
}

func (v *StringValue) valueType() ValueType {
	return ValueTypeString
}

func (v *StringValue) setOrigin(origin parse.Node) {
	v.origin = origin
}

func (v *StringValue) OriginNode() parse.Node {
	return v.origin
}

func (v *StringValue) CopyWithOrigin(origin parse.Node) Value {
	return &StringValue{
		origin: origin,
		value:  v.value,
	}
}

func (v *StringValue) RawGNString() string {
	return v.value
}

func (v *StringValue) Equal(other Value) bool {
	if other, ok := other.(*StringValue); ok {
		return v.value == other.value
	}
	return false
}

// starlark.Value interface.

func (v StringValue) String() string        { return starsyntax.Quote(v.value, false) }
func (v StringValue) Type() string          { return "gnstring" }
func (StringValue) Freeze()                 {} // immutable
func (v StringValue) Truth() starlark.Bool  { return len(v.value) > 0 }
func (v StringValue) Hash() (uint32, error) { return starlark.String(v.value).Hash() }
