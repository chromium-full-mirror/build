// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"fmt"
	"iter"
	"maps"
	"slices"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

// ExecContext is the execution context for a scope, and may also hold a
// scope of its own to be used as a top-level read-only value source.
//
// Implementations are expected to be safe for concurrent read access.
type ExecContext interface {
	BaseConfig() *Scope
}

// Scope for the script execution.
//
// Scopes are nested. Writing goes into the current scope, reading checks
// values through containing scopes until a match is found or there are no
// more containing scopes.
//
// Unlike C++ GN, all scopes are considered "non-const scopes".
// The closest analogue to a "const scope" is that a scope here may reference
// an ExecContext object, like the `Settings` object that scopes in C++ GN
// will reference.
// When reading values, the base config returned by the ExecContext will be
// checked as a last-resort, and no mutate operations will not be performed on
// that scope.
type Scope struct {
	execContext    ExecContext
	skipBaseConfig bool
	parent         *Scope

	values map[string]record
}

type record struct {
	used  bool // Set to true when the variable is used.
	value Value
}

// isolate makes this scope isolated when resolving variables, in other words
// will force all variable resolution to happen in this scope only. If this
// scope references a ExecContext object, that will also be ignored.
//
// This is useful when returning a BlockNode as a value, as it should not be
// able to reference outside values. (However, it should still hold a reference
// to the execution context.)
func (s *Scope) isolate() {
	s.parent = nil
	s.skipBaseConfig = true
}

// NewScopeFromExecContext creates a scope dependent on a ExecContext.
func NewScopeFromExecContext(c ExecContext) *Scope {
	return &Scope{
		execContext: c,
		values:      make(map[string]record),
	}
}

// NewNestedScope creates a dependent scope.
func (s *Scope) NewNestedScope() *Scope {
	return &Scope{
		parent:      s,
		execContext: s.execContext,
		values:      make(map[string]record),
	}
}

// HasValues returns whether this scope has values set.
func (s *Scope) HasValues() bool {
	return len(s.values) > 0
}

// Value gets the value with the ident in the current scope if found,
// otherwise recursively searches containing scopes until a match is found
// or there are no more containing scopes.
//
// markAsUsed should be set if the variable is being read in a way that should
// count for unused variable checking.
func (s *Scope) Value(ident string, markAsUsed bool) Value {
	if value, found := s.values[ident]; found {
		if markAsUsed {
			value.used = true
			s.values[ident] = value
		}
		return value.value
	}

	// Search in the containing scope.
	if s.parent != nil {
		return s.parent.Value(ident, markAsUsed)
	}

	// If there is no containing scope, search the base config.
	if !s.skipBaseConfig && s.execContext != nil {
		return s.execContext.BaseConfig().Value(ident, false)
	}

	return nil
}

// valuesInCurrentScope returns an iterator over the values in the current scope
// in sorted order.
func (s *Scope) valuesInCurrentScope() iter.Seq2[string, Value] {
	return func(yield func(string, Value) bool) {
		for _, ident := range slices.Sorted(maps.Keys(s.values)) {
			record := s.values[ident]
			if !yield(ident, record.value) {
				return
			}
		}
	}
}

// SetValue sets the value in the current scope with the origin node for error reporting purposes.
func (s *Scope) SetValue(ident string, v Value, setNode parse.Node) {
	s.values[ident] = record{
		used:  false,
		value: v.CopyWithOrigin(setNode),
	}
}

// CheckForUnusedVars checks the scope to see if any values were set but not used, and fills in
// the error if they were.
func (s *Scope) CheckForUnusedVars() error {
	// To maintain behavioral compatibility with C++ GN, sort the map by keys first.
	for _, ident := range slices.Sorted(maps.Keys(s.values)) {
		record := s.values[ident]
		if !record.used {
			help := fmt.Sprintf("You set the variable %q here and it was unused before it went out of scope.", ident)
			binary, ok := record.value.OriginNode().(*parse.BinaryOpNode)
			if ok && binary.Op.TokenType() == syntax.TokenEqual {
				// Make a nicer error message for normal var sets.
				return syntax.MakeErrorAt(binary.Left.LocationRange().Begin(), nil, syntax.ErrUselessAssignment, "Assignment had no effect.", help)
			}
			return parse.MakeErrFromNode(record.value.OriginNode(), syntax.ErrUselessAssignment, "Assignment had no effect.", help)
		}
	}
	return nil
}

// checkCurrentScopeValuesEqual returns true if the values in the current scope are the same as all
// values in the given scope, without going to the parent scopes. Returns false if not.
func (s *Scope) checkCurrentScopeValuesEqual(other *Scope) bool {
	// C++ GN fails equality if there's "containing" scopes.
	if s.parent != nil {
		return false
	}

	// But we also fallback to the "base config" if it exists, which in C++ GN
	// is instead just treated as a containing scope.
	// So we have to check for that too.
	if !s.skipBaseConfig && s.execContext != nil {
		return false
	}

	// Now continue to follow the original C++ GN.
	if len(s.values) != len(other.values) {
		return false
	}
	for ident, record := range s.values {
		v := other.Value(ident, false)
		if v == nil || !v.Equal(record.value) {
			return false
		}
	}
	return true
}
