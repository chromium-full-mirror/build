// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package cpu provides information regarding the cpu of the host machine.
package cpu

// LogicalCores returns the number of logical cores usable by the current process.
func LogicalCores() int {
	return logicalCores()
}
