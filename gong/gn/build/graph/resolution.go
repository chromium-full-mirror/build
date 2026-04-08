// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"iter"
	"maps"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
)

// ResolverContext provides context to a [ResolverFn], allowing only indirect access to underlying target data.
//
// TODO: this list of functions one for each type of variable is starting to look a bit silly.
type ResolverContext struct {
	// ConfigValues returns the config values for this target.
	ConfigValues ConfigValues
	// DeclareTool declares a tool call.
	DeclareTool func(tool string, source fs.SourceFile, inputs []fs.SourceFile, outputName string, expansions Expansions) (fs.OutputPath, error)
	// DeclareScript declares a script call.
	DeclareScript func(script fs.SourceFile, args, outputNames []string, inputs []fs.SourceFile, depfile string, rspfileContent []string) ([]fs.OutputPath, error)
	// LabelKeyedStringMapFor returns the map of labels to strings for the variable, if it accepts
	// a variable that is processed into a map of labels to strings.
	LabelKeyedStringMapFor func(varName string) (map[environment.Label]string, error)
	// StringFor returns the string for the variable, if it accepts strings.
	StringFor func(varName string) (string, error)
	// StringsFor returns an iterator over strings for the variable, if it accepts string lists.
	StringsFor func(varName string) iter.Seq2[string, error]
	// BoolFor returns the boolean for the variable, if it accepts booleans.
	BoolFor func(varName string) (bool, error)
	// SourceFileFor returns the source file for the variable, if it accepts a file.
	SourceFileFor func(varName string) (fs.SourceFile, error)
	// SourceFilesFor returns an iterator over source files for the variable, if it accepts file lists.
	// TODO: maybe should be (iter.Seq2[fs.SourceFile, error], error)
	// or just return nil iterator if varName doesn't exist?
	// Otherwise, can't easily distinguish between failure iterating next sourcefile and
	// error because varName doesn't exist.
	SourceFilesFor func(varName string) iter.Seq2[fs.SourceFile, error]
	// ResolvedTargetsFor returns an iterator over resolutions for the variable, if it accepts target lists.
	ResolvedTargetsFor func(varName string) iter.Seq2[Resolution, error]
}

// A ResolverFn tries to resolve a target.
// It returns an error instead if processing fails.
type ResolverFn = func(ResolverContext) (ResolutionMetadata, error)

// A Resolution of a target records the actions that a target performs, and any metadata that
// may be relevant to targets waiting for this target to be resolved.
type Resolution struct {
	Actions  []Action
	Label    environment.Label
	Metadata ResolutionMetadata
}

// An Action represents anything a target does that results in build graph outputs.
// This is not to be confused with GN's action() target type.
type Action interface {
	Ins() []fs.SourceFile
}

// Expansions represents expansions to a GN tool call.
type Expansions interface {
	// Keys returns an iterator over the keys of the expansions.
	// The iteration order is not specified and is not guaranteed to be the same
	// from one call to the next.
	Keys() iter.Seq[string]
	// Value returns the value for the given key, if it exists.
	Value(k string) ([]string, bool)
}

// SimpleExpansions represents a basic set of expansions to a GN tool call.
type SimpleExpansions struct {
	Elems map[string][]string
}

func (s *SimpleExpansions) Keys() iter.Seq[string] {
	return maps.Keys(s.Elems)
}

func (s *SimpleExpansions) Value(k string) (v []string, ok bool) {
	v, ok = s.Elems[k]
	return
}

// CompositeExpansions represents a set of expansions to a GN tool call
// that shares a common set of expansions.
type CompositeExpansions struct {
	Common *SimpleExpansions
	Elems  map[string][]string
}

func (c *CompositeExpansions) Keys() iter.Seq[string] {
	return func(yield func(string) bool) {
		seen := make(map[string]struct{})
		for k := range c.Common.Elems {
			seen[k] = struct{}{}
			if !yield(k) {
				return
			}
		}
		for k := range c.Elems {
			if _, ok := seen[k]; ok {
				continue
			}
			if !yield(k) {
				return
			}
		}
	}
}

func (c *CompositeExpansions) Value(k string) ([]string, bool) {
	if v, ok := c.Elems[k]; ok {
		return v, true
	}
	return c.Common.Value(k)
}

// A RunToolAction represents a call to a tool inside the current toolchain.
type RunToolAction struct {
	Tool       string
	Source     fs.SourceFile
	Inputs     []fs.SourceFile
	Output     fs.OutputPath
	Expansions Expansions
}

func (r RunToolAction) Ins() []fs.SourceFile { return r.Inputs }

// A RunScriptAction represents a script call.
type RunScriptAction struct {
	Script         fs.SourceFile
	Args           []SubstitutionPattern // TODO: port SubstitutionList so validation can be performed.
	Outputs        []fs.OutputPath
	Inputs         []fs.SourceFile
	Depfile        string
	RspfileContent []SubstitutionPattern
}

func (r RunScriptAction) Ins() []fs.SourceFile { return r.Inputs }

// ResolutionMetadata represents metadata provided by target resolvers,
// that dependent target resolvers may access.
// A target resolver must at minimum return its outputs, but can choose to provide
// additional metadata.
type ResolutionMetadata interface {
	// Outputs returns the output(s) from this target.
	Outputs() []fs.OutputPath
}
