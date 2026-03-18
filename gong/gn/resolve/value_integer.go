// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"strconv"

	"go.starlark.net/starlark"

	"go.chromium.org/build/gong/gn/parse"
)

// IntegerValue represents a GN integer, which is 64-bit.
type IntegerValue struct {
	origin parse.Node
	value  int64
}

// NewOriginlessIntegerValue creates an integer value without an origin.
func NewOriginlessIntegerValue(value int64) *IntegerValue {
	return &IntegerValue{value: value}
}

func (v *IntegerValue) valueType() ValueType {
	return ValueTypeInteger
}

func (v *IntegerValue) setOrigin(origin parse.Node) {
	v.origin = origin
}

func (v *IntegerValue) OriginNode() parse.Node {
	return v.origin
}

func (v *IntegerValue) CopyWithOrigin(origin parse.Node) Value {
	return &IntegerValue{
		origin: origin,
		value:  v.value,
	}
}

func (v *IntegerValue) RawGNString() string {
	return strconv.FormatInt(v.value, 10)
}

func (v *IntegerValue) Equal(other Value) bool {
	if other, ok := other.(*IntegerValue); ok {
		return v.value == other.value
	}
	return false
}

func (v *IntegerValue) Value() int64 {
	return v.value
}

// starlark.Value interface.

func (v IntegerValue) String() string        { return strconv.FormatInt(v.value, 10) }
func (v IntegerValue) Type() string          { return "gnint" }
func (IntegerValue) Freeze()                 {} // immutable
func (v IntegerValue) Truth() starlark.Bool  { return v.value != 0 }
func (v IntegerValue) Hash() (uint32, error) { return starlark.MakeInt64(v.value).Hash() }
