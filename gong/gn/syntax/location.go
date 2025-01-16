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
