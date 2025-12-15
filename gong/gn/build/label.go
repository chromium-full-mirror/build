// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

// Label represents the name of a target or some other named thing in
// the source path. The label is always absolute and always includes a name
// part, so it starts with a slash, and has one colon.
type Label struct {
	dir  string
	name string //nolint:unused // keep for future use
}
