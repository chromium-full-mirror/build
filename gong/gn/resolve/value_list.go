// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"fmt"
	"iter"
	"slices"
	"strings"

	"go.starlark.net/starlark"

	"go.chromium.org/build/gong/gn/parse"
)

// ListValue represents a GN list.
type ListValue struct {
	origin parse.Node
	list   []Value
}

// NewOriginlessListValue creates a list value without an origin.
func NewOriginlessListValue(list []Value) *ListValue {
	return &ListValue{list: list}
}

func (v *ListValue) access(index int64, origin parse.Node) (valueDestination, error) {
	if len(v.list) == 0 || index < 0 || index >= int64(len(v.list)) {
		return nil, SubscriptError{
			OriginNode: parse.OriginNode{Node: origin},
			index:      index,
			len:        len(v.list),
		}
	}
	return listValue{
		list:  v,
		index: index,
	}, nil
}

// listValue represents a lvalue access of a list's values.
type listValue struct {
	list  *ListValue
	index int64
}

// assign performs the action of mutating a list's value.
// It implements valueDestination, hence takes an origin AST node.
// However the origin AST node is ignored, which matches C++ GN behavior.
func (a listValue) assign(newValue Value, _ parse.Node) Value {
	a.list.list[a.index] = newValue
	return newValue
}

// ensureValue implements valueDestination.
func (a listValue) ensureValue() error {
	// Because a listValue isn't returned by `func (v *ListValue) access` unless the
	// subscript is valid, we can assume something's gone wrong if the value is out
	// of bounds.
	if a.list == nil {
		return fmt.Errorf("internal error: listValue pointing at nil value")
	}
	if a.index < 0 || a.index >= int64(len(a.list.list)) {
		return fmt.Errorf("internal error: listValue pointing at invalid index")
	}
	return nil
}

// valueForValidation returns the current Value this list access `a[b]` represents,
// such that operations can check whether an assignment operation `a[b] = c` is legal.
func (a listValue) valueForValidation() Value {
	return a.list.list[a.index]
}

// valueForMutation returns the current Value this list access `a[b]` represents,
// such that operations can perform a mutation on it.
func (a listValue) valueForMutation(origin parse.Node) Value {
	// C++ GN does not use the origin, so we also ignore it.
	return a.list.list[a.index]
}

func (v *ListValue) valueType() ValueType {
	return ValueTypeList
}

func (v *ListValue) setOrigin(origin parse.Node) {
	v.origin = origin
}

func (v *ListValue) OriginNode() parse.Node {
	return v.origin
}

func (v *ListValue) CopyWithOrigin(origin parse.Node) Value {
	return &ListValue{
		origin: origin,
		list:   v.list,
	}
}

func (v *ListValue) RawGNString() string {
	var result strings.Builder
	result.WriteString("[")
	for i, value := range v.list {
		if value == v {
			// Handle edge case where self-referential lists are possible.
			// C++ GN is not susceptible to self-referential lists
			// because lists store a std::vector<Value>, where "Value"
			// is a concrete type.
			return "[...]"
		}
		if i > 0 {
			result.WriteString(", ")
		}
		result.WriteString(GNLiteralRvalue(value))
	}
	result.WriteString("]")
	return result.String()
}

// Values returns an iterator over the list's values.
func (v *ListValue) Values() iter.Seq[Value] {
	return slices.Values(v.list)
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

func (v *ListValue) Equal(other Value) bool {
	otherList, ok := other.(*ListValue)
	if !ok {
		return false
	}
	if len(v.list) != len(otherList.list) {
		return false
	}
	for i, item := range v.list {
		if !item.Equal(otherList.list[i]) {
			return false
		}
	}
	return true
}

// starlark.Sequences interface.

func (v *ListValue) Len() int { return len(v.list) }

// starlark.Indexable interface.

func (v *ListValue) Index(i int) starlark.Value {
	if i < 0 || i >= len(v.list) {
		return starlark.None
	}
	return v.list[i]
}
