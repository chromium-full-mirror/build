// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"iter"

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
	DeclareTool func(tool string, source fs.SourceFile, inputs []fs.SourceFile, outputName string, expansions map[string]string) (fs.OutputPath, error)
	// LabelKeyedStringMapFor returns the map of labels to strings for the variable, if it accepts
	// a variable that is processed into a map of labels to strings.
	LabelKeyedStringMapFor func(varName string) (map[environment.Label]string, error)
	// StringFor returns the string for the variable, if it accepts strings.
	StringFor func(varName string) (string, error)
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
	Actions  []RunToolAction
	Label    environment.Label
	Metadata ResolutionMetadata
}

// A RunToolAction represents a call to a tool inside the current toolchain.
type RunToolAction struct {
	Tool       string
	Source     fs.SourceFile
	Inputs     []fs.SourceFile
	Output     fs.OutputPath
	Expansions map[string]string
}

// ResolutionMetadata represents metadata provided by target resolvers,
// that dependent target resolvers may access.
// A target resolver must at minimum return its outputs, but can choose to provide
// additional metadata.
type ResolutionMetadata interface {
	// Outputs returns the output(s) from this target.
	Outputs() []fs.OutputPath
}
