// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package syntax

import (
	"infra/build/gong/gn/fs"
)

// Location represents a place in a source file. Used for error reporting.
type Location struct {
	file         *fs.InputFile
	lineNumber   int // 0 when unset. 1-based.
	columnNumber int // 0 when unset. 1-based.
}

// LocationRange represents a range in a source file. Used for error reporting.
// The end is exclusive i.e. [begin, end)
type LocationRange struct {
	begin Location
	end   Location
}
