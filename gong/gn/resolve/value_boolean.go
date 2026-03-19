// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"go.starlark.net/starlark"

	"go.chromium.org/build/gong/gn/parse"
)

// BooleanValue represents a GN boolean.
type BooleanValue struct {
	origin parse.Node
	value  bool
}

func (v *BooleanValue) valueType() ValueType {
	return ValueTypeBoolean
}

// Value returns the boolean value.
func (v *BooleanValue) Value() bool {
	return v.value
}

func (v *BooleanValue) setOrigin(origin parse.Node) {
	v.origin = origin
}

func (v *BooleanValue) OriginNode() parse.Node {
	return v.origin
}

func (v *BooleanValue) CopyWithOrigin(origin parse.Node) Value {
	return &BooleanValue{
		origin: origin,
		value:  v.value,
	}
}

func (v *BooleanValue) RawGNString() string {
	if v.value {
		return "true"
	}
	return "false"
}

func (v *BooleanValue) Equal(other Value) bool {
	if other, ok := other.(*BooleanValue); ok {
		return v.value == other.value
	}
	return false
}

// starlark.Value interface.

func (v BooleanValue) String() string {
	if v.value {
		return "True"
	}
	return "False"
}
func (v BooleanValue) Type() string          { return "gnbool" }
func (BooleanValue) Freeze()                 {} // immutable
func (v BooleanValue) Truth() starlark.Bool  { return starlark.Bool(v.value) }
func (v BooleanValue) Hash() (uint32, error) { return starlark.Bool(v.value).Hash() }
