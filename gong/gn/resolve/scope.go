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

// isPrivateVar returns true if this variable name should be considered private.
// Private values start with an underscore, and are not imported from "gni" files
// when processing an import.
//
// Note that empty identifiers are not valid, however for purposes of matching
// C++ GN behavior at time of writing, passing an empty string will return true.
func isPrivateVar(name string) bool {
	return len(name) == 0 || name[0] == '_'
}

// ExecContext is the execution context for a scope, and may also reference
// a scope to be used as a top-level read-only value source.
//
// Implementations are expected to be safe for concurrent read access.
type ExecContext interface {
	// BaseConfig returns the top-level read-only scope.
	// It is called as a read-only last resort when resolving variables.
	BaseConfig() *Scope
	// NestedContext creates a new nested context for the given scope.
	// It is called when creating a nested scope.
	NestedContext() ExecContext
}

// ProgrammaticProvider allows code to provide values for built-in variables.
//
// TODO: should this be merged with ExecContext?
type ProgrammaticProvider interface {
	// ProgrammaticBuiltin returns (Value, true) if the given value can be programmatically
	// generated, or (nil, false) if there is none.
	ProgrammaticBuiltin(ident string) (Value, bool)
}

// Scope for the script execution.
//
// Scopes are nested. Writing goes into the current scope, reading checks
// values through containing scopes until a match is found or there are no
// more containing scopes.
//
// Unlike C++ GN, all scopes are considered "non-const scopes".
// The closest analogue to a "const scope" is that a scope here may reference
// an ExecContext's BaseConfig(), like the `Settings` object that scopes in C++ GN
// will reference.
// When reading values, the ExecContext's BaseConfig() will then be checked
// as a last-resort, and we avoid performing direct mutate operations on it.
type Scope struct {
	execContext          ExecContext
	programmaticProvider ProgrammaticProvider
	skipBaseConfig       bool
	parent               *Scope
	// functions is a map from names to GN functions and/or GN templates.
	functions map[string]FunctionInfo

	values map[string]record
}

// ScopeMergeOptions configures merges from one scope into another.
type ScopeMergeOptions struct {
	// SourceNode will be presented as the source location of the merge for error reporting.
	SourceNode parse.Node
	// SourceFriendlyName will be presented as the source name of the merge for error reporting.
	SourceFriendlyName string
	// DestinationMarkUsed will mark values copied to the destination scope as used
	// so won't trigger an unused variable warning. You want this when doing an
	// import, for example, or files that don't need a variable from the .gni
	// file will throw an error.
	DestinationMarkUsed bool
	// DestinationClobber will overwrite values in the destination scope if they
	// already exist.
	//
	// When false, it will be an error to merge a variable into another scope
	// where a variable with the same name is already set. The exception is
	// if both of the variables have the same value (which happens if you
	// somehow multiply import the same file, for example). This case will be
	// ignored since there is nothing getting lost.
	DestinationClobber bool
	// SkipPrivateVars will skip private variables (names beginning with an underscore)
	// when copying to the destination scope.
	SkipPrivateVars bool
	// ExcludedValues will exclude the given values when copying to the destination scope.
	ExcludedValues map[string]struct{}
}

type record struct {
	used  bool // Set to true when the variable is used.
	value Value
}

func (s *Scope) access(name syntax.Token) valueDestination {
	return scopeAccess{
		scope: s,
		name:  name,
	}
}

// scopeAccess represents a lvalue access of a scope's values.
type scopeAccess struct {
	scope *Scope
	name  syntax.Token
}

// ensureValue implements valueDestination.
func (a scopeAccess) ensureValue() error {
	if a.scope.Value(a.name.Value(), false) == nil {
		return UndefinedIdentifierError{OriginToken: syntax.OriginToken{Token: a.name}}
	}
	return nil
}

// assign performs the action of mutating a scope's value.
// It implements valueDestination.
func (a scopeAccess) assign(newValue Value, origin parse.Node) Value {
	a.scope.values[a.name.Value()] = record{
		used:  false,
		value: newValue.CopyWithOrigin(origin),
	}
	return newValue
}

// valueForValidation returns the current Value this scope access `a.b` represents,
// such that operations can check whether an assignment operation `a.b = c` is legal.
func (a scopeAccess) valueForValidation() Value {
	return a.scope.Value(a.name.Value(), true)
}

// valueForMutation returns the current Value this scope access `a.b` represents,
// such that operations can perform a mutation on it.
func (a scopeAccess) valueForMutation(origin parse.Node) Value {
	if r, found := a.scope.values[a.name.Value()]; found {
		// The value will be written to, reset its tracking information.
		newValue := r.value.CopyWithOrigin(origin)
		a.scope.values[a.name.Value()] = record{
			used:  false,
			value: newValue,
		}
		return newValue
	}
	return nil
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

// NewScope creates a top-level scope.
func NewScope(c ExecContext, programmaticProvider ProgrammaticProvider, functions map[string]FunctionInfo) *Scope {
	return &Scope{
		execContext:          c,
		programmaticProvider: programmaticProvider,
		functions:            functions,
		values:               make(map[string]record),
	}
}

// NewNestedScope creates a dependent scope whose parent is this scope.
func (s *Scope) NewNestedScope() *Scope {
	if s.execContext == nil {
		return &Scope{
			parent: s,
			values: make(map[string]record),
		}
	}
	return &Scope{
		parent:      s,
		execContext: s.execContext.NestedContext(),
		values:      make(map[string]record),
	}
}

// HasValues returns whether this scope has values set.
func (s *Scope) HasValues() bool {
	return len(s.values) > 0
}

// ExecContext returns the execution context for this scope.
func (s *Scope) ExecContext() ExecContext {
	return s.execContext
}

// Value gets the value with the ident in the current scope if found,
// otherwise recursively searches containing scopes until a match is found
// or there are no more containing scopes.
//
// markAsUsed should be set if the variable is being read in a way that should
// count for unused variable checking.
func (s *Scope) Value(ident string, markAsUsed bool) Value {
	if s.programmaticProvider != nil {
		if v, ok := s.programmaticProvider.ProgrammaticBuiltin(ident); ok {
			return v
		}
	}

	// Search in the current scope.
	value := s.valueInCurrentScope(ident, markAsUsed)
	if value != nil {
		return value
	}

	// Search in the containing scope.
	if s.parent != nil {
		return s.parent.Value(ident, markAsUsed)
	}

	// If there is no containing scope, search the base config.
	if !s.skipBaseConfig && s.execContext != nil {
		baseConfig := s.execContext.BaseConfig()
		if baseConfig != s {
			return baseConfig.Value(ident, false)
		}
	}

	return nil
}

func (s *Scope) valueInCurrentScope(ident string, markAsUsed bool) Value {
	if value, found := s.values[ident]; found {
		if markAsUsed {
			value.used = true
			s.values[ident] = value
		}
		return value.value
	}
	return nil
}

// function gets the function with the ident in the current scope if found,
// otherwise recursively searches containing scopes until a match is found
// or there are no more containing scopes.
func (s *Scope) function(name string) (FunctionInfo, bool) {
	if f, found := s.functions[name]; found {
		return f, true
	}

	// Search in the containing scope.
	if s.parent != nil {
		return s.parent.function(name)
	}

	// If there is no containing scope, search the base config.
	if !s.skipBaseConfig && s.execContext != nil {
		return s.execContext.BaseConfig().function(name)
	}

	return nil, false
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

// NonRecursiveMergeTo copies this scope's values into the destination.
// Values from the containing scope(s) (normally shadowed into the current one)
// will not be copied, neither will the reference to the containing scope (this
// is why it's "non-recursive").
func (s *Scope) NonRecursiveMergeTo(dest *Scope, options ScopeMergeOptions) error {
	for currentName, rec := range s.values {
		if options.SkipPrivateVars && isPrivateVar(currentName) {
			continue // Skip this private var.
		}
		if _, ok := options.ExcludedValues[currentName]; ok {
			continue // Skip this excluded value.
		}

		newValue := rec.value
		if !options.DestinationClobber {
			existingValue := dest.Value(currentName, false)
			if existingValue != nil && !newValue.Equal(existingValue) {
				// Value present in both the source and the dest.
				// TODO: add extra help text that points to what's being clobbered.
				return parse.MakeErrFromNode(options.SourceNode, syntax.ErrInvalidOperation,
					"Value collision.",
					fmt.Sprintf("This %s contains %q", options.SourceFriendlyName, currentName))
			}
		}

		dest.values[currentName] = record{
			used:  options.DestinationMarkUsed,
			value: newValue,
		}
	}

	return nil
}
