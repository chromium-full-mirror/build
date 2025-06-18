// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"fmt"
	"strings"

	"go.starlark.net/starlark"

	"go.chromium.org/build/gong/gn/parse"
)

// ListValue represents a GN list.
type ListValue struct {
	origin parse.ParseNode
	list   []Value
}

func (v *ListValue) valueType() ValueType {
	return ValueTypeList
}

func (v *ListValue) setOrigin(origin parse.ParseNode) {
	v.origin = origin
}

func (v *ListValue) OriginNode() parse.ParseNode {
	return v.origin
}

func (v *ListValue) RawGNString() string {
	result := "["
	for i, value := range v.list {
		if value == v {
			// Handle edge case where self-referential lists are possible.
			// C++ GN is not susceptible to self-referential lists
			// because lists store a std::vector<Value>, where "Value"
			// is a concrete type.
			return "[...]"
		}
		if i > 0 {
			result += ", "
		}
		result += GNLiteralRvalue(value)
	}
	return result + "]"
}

// starlark.Value interface.

func (v *ListValue) String() string {
	out := new(strings.Builder)
	out.WriteByte('[')
	for i, value := range v.list {
		if value == v {
			// Match Starlark self-referential list output.
			return "[...]"
		}
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(value.String())
	}
	out.WriteByte(']')
	return out.String()
}
func (v *ListValue) Type() string          { return "gnlist" }
func (*ListValue) Freeze()                 {} // GN lists are not mutable from Starlark.
func (v *ListValue) Truth() starlark.Bool  { return len(v.list) > 0 }
func (v *ListValue) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: gnlist") }

// starlark.Iterable interface.

func (v *ListValue) Iterate() starlark.Iterator {
	return &listIterator{
		listValue: v,
	}
}

type listIterator struct {
	listValue *ListValue
	i         int
}

func (it *listIterator) Next(v *starlark.Value) bool {
	if it.i < len(it.listValue.list) {
		*v = starlark.Value(it.listValue.list[it.i])
		it.i++
		return true
	}
	return false
}

func (it *listIterator) Done() {}

// starlark.Sequences interface.

func (v *ListValue) Len() int { return len(v.list) }

// starlark.Indexable interface.

func (v *ListValue) Index(i int) starlark.Value {
	if i < 0 || i >= len(v.list) {
		return starlark.None
	}
	return v.list[i]
}
