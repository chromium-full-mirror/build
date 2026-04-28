// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package memory provides a way to get the total physical memory of the machine.
package memory

// Total returns the total amount of physical memory on the machine in bytes.
func Total() (uint64, error) {
	return total()
}
