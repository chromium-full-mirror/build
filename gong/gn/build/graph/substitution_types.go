// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

// substitutionPart represents a part of a substitution pattern in GN and Ninja.
type substitutionPart interface {
	// String is the GN representation, e.g. "{{response_file_name}}"
	String() string
	// NinjaString is the Ninja representation, e.g. "${rspfile}"
	NinjaString() string
}

// substitutionLiteral is a literal string inside a substitution pattern.
type substitutionLiteral struct {
	Literal string
}

func (s substitutionLiteral) String() string      { return s.Literal }
func (s substitutionLiteral) NinjaString() string { return s.Literal }

// substitutionVar represents a variable substitution in GN and Ninja.
type substitutionVar struct {
	GN, Ninja string
}

func (s substitutionVar) String() string      { return s.GN }
func (s substitutionVar) NinjaString() string { return s.Ninja }

var substitutionSource = substitutionVar{"{{source}}", "${in}"}
var substitutionOutput = substitutionVar{"{{output}}", "${out}"}

var generalSubstitutions = []substitutionPart{
	substitutionSource,
	substitutionOutput,
}
