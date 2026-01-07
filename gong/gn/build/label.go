// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"strings"

	"go.chromium.org/build/gong/gn/build/fs"
)

// Label represents the name of a target or some other named thing in
// the source path. The label is always absolute and always includes a name
// part, so it starts with a slash, and has one colon.
type Label struct {
	dir           fs.SourceDir
	name          string
	toolchainDir  fs.SourceDir
	toolchainName string
}

// UserVisibleString formats this label in a way that we can present to the user
// or expose to other parts of the system. Source directories end in slashes,
// but the user expects names like "//chrome/renderer:renderer_config" when
// printed. The toolchain is optionally included.
func (l Label) UserVisibleString(includeToolchain bool) string {
	var sb strings.Builder
	sb.Grow(len(l.dir.Path()) + len(l.name) + 1)
	if l.dir.Empty() {
		return ""
	}
	sb.WriteString(l.dir.WithNoTrailingSlash())
	sb.WriteRune(':')
	sb.WriteString(l.name)
	if includeToolchain {
		sb.WriteRune('(')
		if !l.toolchainDir.Empty() && l.toolchainName != "" {
			sb.WriteString(l.toolchainDir.WithNoTrailingSlash())
			sb.WriteRune(':')
			sb.WriteString(l.toolchainName)
		}
		sb.WriteRune(')')
	}
	return sb.String()
}
