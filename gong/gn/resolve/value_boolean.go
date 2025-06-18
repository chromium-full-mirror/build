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
	origin parse.ParseNode
	value  bool
}

func (v *BooleanValue) valueType() ValueType {
	return ValueTypeBoolean
}

func (v *BooleanValue) setOrigin(origin parse.ParseNode) {
	v.origin = origin
}

func (v *BooleanValue) OriginNode() parse.ParseNode {
	return v.origin
}

func (v *BooleanValue) RawGNString() string {
	if v.value {
		return "true"
	}
	return "false"
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
