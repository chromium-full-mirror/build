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
	origin parse.ParseNode
	value  string
}

func (v *StringValue) valueType() ValueType {
	return ValueTypeString
}

func (v *StringValue) setOrigin(origin parse.ParseNode) {
	v.origin = origin
}

func (v *StringValue) OriginNode() parse.ParseNode {
	return v.origin
}

func (v *StringValue) RawGNString() string {
	return v.value
}

// starlark.Value interface.

func (v StringValue) String() string        { return starsyntax.Quote(v.value, false) }
func (v StringValue) Type() string          { return "gnstring" }
func (StringValue) Freeze()                 {} // immutable
func (v StringValue) Truth() starlark.Bool  { return len(v.value) > 0 }
func (v StringValue) Hash() (uint32, error) { return starlark.String(v.value).Hash() }
