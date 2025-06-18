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
	origin parse.ParseNode
	value  int64
}

func (v *IntegerValue) valueType() ValueType {
	return ValueTypeInteger
}

func (v *IntegerValue) setOrigin(origin parse.ParseNode) {
	v.origin = origin
}

func (v *IntegerValue) OriginNode() parse.ParseNode {
	return v.origin
}

func (v *IntegerValue) RawGNString() string {
	return strconv.FormatInt(v.value, 10)
}

// starlark.Value interface.

func (v IntegerValue) String() string        { return strconv.FormatInt(v.value, 10) }
func (v IntegerValue) Type() string          { return "gnint" }
func (IntegerValue) Freeze()                 {} // immutable
func (v IntegerValue) Truth() starlark.Bool  { return v.value != 0 }
func (v IntegerValue) Hash() (uint32, error) { return starlark.MakeInt64(v.value).Hash() }
