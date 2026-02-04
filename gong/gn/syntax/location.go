// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package syntax

import (
	"fmt"
)

// Location represents a place in a source file. Used for error reporting.
type Location struct {
	file         InputSource
	lineNumber   int // 0 when unset. 1-based.
	columnNumber int // 0 when unset. 1-based.
}

// Equal returns if the two locations are equal.
func (l Location) Equal(other Location) bool {
	return l.file.Equal(other.file) && l.lineNumber == other.lineNumber && l.columnNumber == other.columnNumber
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
	if l.file == nil {
		return ""
	}
	if !includeColumnNumber {
		return fmt.Sprintf("%s:%d", l.file.DisplayName(), l.lineNumber)
	}
	return fmt.Sprintf("%s:%d:%d", l.file.DisplayName(), l.lineNumber, l.columnNumber)
}

// LocationRange represents a range in a source file. Used for error reporting.
// The end is exclusive i.e. [begin, end)
type LocationRange struct {
	begin Location
	end   Location
}

// Equal returns if the two location ranges are equal.
func (l LocationRange) Equal(other LocationRange) bool {
	return l.begin.Equal(other.begin) && l.end.Equal(other.end)
}

// Union returns a location range combined with the current location range.
// Returns blank if the files are not the same.
func (l LocationRange) Union(other LocationRange) LocationRange {
	if l.begin.file != nil && other.begin.file != nil && !l.begin.file.Equal(other.begin.file) {
		return LocationRange{}
	}
	return LocationRange{l.begin.min(other.begin), l.end.max(other.end)}
}

// Begin returns the beginning of this location range.
func (l LocationRange) Begin() Location {
	return l.begin
}
