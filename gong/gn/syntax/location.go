// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package syntax

import (
	"fmt"

	"infra/build/gong/gn/fs"
)

// Location represents a place in a source file. Used for error reporting.
type Location struct {
	file         *fs.InputFile
	lineNumber   int // 0 when unset. 1-based.
	columnNumber int // 0 when unset. 1-based.
}

func (l Location) min(other Location) Location {
	if l.file != other.file {
		return Location{}
	}
	if l.lineNumber < other.lineNumber || (l.lineNumber == other.lineNumber && l.columnNumber < other.columnNumber) {
		return l
	}
	return other
}

func (l Location) max(other Location) Location {
	if l.file != other.file {
		return Location{}
	}
	if l.lineNumber > other.lineNumber || (l.lineNumber == other.lineNumber && l.columnNumber > other.columnNumber) {
		return l
	}
	return other
}

// LineNumber returns the line number of the location.
func (l Location) LineNumber() int {
	return l.lineNumber
}

// ColumnNumber returns the column number of the location.
func (l Location) ColumnNumber() int {
	return l.columnNumber
}

// Describe returns a string representation of the location.
func (l Location) Describe(includeColumnNumber bool) string {
	name := l.file.Name.Filename()
	if !includeColumnNumber {
		return fmt.Sprintf("%s:%d", name, l.lineNumber)
	}
	return fmt.Sprintf("%s:%d:%d", name, l.lineNumber, l.columnNumber)
}

// LocationRange represents a range in a source file. Used for error reporting.
// The end is exclusive i.e. [begin, end)
type LocationRange struct {
	begin Location
	end   Location
}

// Union returns a location range combined with the current location range.
// Returns blank if the files are not the same.
func (l LocationRange) Union(other LocationRange) LocationRange {
	if l.begin.file != other.begin.file {
		return LocationRange{}
	}
	return LocationRange{l.begin.min(other.begin), l.end.max(other.end)}
}
