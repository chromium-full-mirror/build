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

// ScopeValue represents a GN scope.
type ScopeValue struct {
	origin parse.ParseNode
	scope  *Scope
}

// OriginNode returns the node that made this. May be nil.
func (v *ScopeValue) OriginNode() parse.ParseNode {
	return v.origin
}

func (v *ScopeValue) setOrigin(origin parse.ParseNode) {
	v.origin = origin
}

// CopyWithOrigin performs a shallow copy of the value with a new origin.
func (v *ScopeValue) CopyWithOrigin(origin parse.ParseNode) Value {
	return &ScopeValue{
		origin: origin,
		scope:  v.scope,
	}
}

func (v *ScopeValue) valueType() ValueType {
	return ValueTypeScope
}

// RawGNString returns a GN-like stringification of the value.
func (v *ScopeValue) RawGNString() string {
	if !v.scope.HasValues() {
		return "{ }"
	}
	var sb strings.Builder
	sb.WriteString("{\n")
	for ident, value := range v.scope.valuesInCurrentScope() {
		fmt.Fprintf(&sb, "  %s = %s\n", ident, GNLiteralRvalue(value))
	}
	sb.WriteString("}")
	return sb.String()
}

// starlark.Value interface.

func (v ScopeValue) String() string        { return "<gnscope>" }
func (v ScopeValue) Type() string          { return "gnscope" }
func (ScopeValue) Freeze()                 {} // GN scopes are not mutable from Starlark.
func (v ScopeValue) Truth() starlark.Bool  { return starlark.Bool(v.scope.HasValues()) }
func (v ScopeValue) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: gnscope") }
