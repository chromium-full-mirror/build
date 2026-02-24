// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"iter"

	"go.chromium.org/build/gong/gn/build/fs"
)

// ResolverContext provides context to a [ResolverFn], allowing only indirect access to underlying target data.
type ResolverContext struct {
	// DeclareTool declares a tool call.
	DeclareTool func(tool string, source fs.SourceFile, inputs []fs.SourceFile, outputName string) (fs.SourceFile, error)
	// StringFor returns the string for the variable, if it accepts strings.
	StringFor func(varName string) (string, error)
	// SourceFileFor returns the source file for the variable, if it accepts a file.
	SourceFileFor func(varName string) (fs.SourceFile, error)
	// SourceFilesFor returns an iterator over source files for the variable, if it accepts file lists.
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
	Metadata ResolutionMetadata
}

// A RunToolAction represents a call to a tool inside the current toolchain.
type RunToolAction struct {
	Tool   string
	Source fs.SourceFile
	Inputs []fs.SourceFile
	Output fs.SourceFile
}

// ResolutionMetadata represents metadata provided by target resolvers,
// that dependent target resolvers may access.
// A target resolver must at minimum return its outputs, but can choose to provide
// additional metadata.
type ResolutionMetadata interface {
	// Outputs returns the output(s) from this target.
	Outputs() []fs.SourceFile
}
