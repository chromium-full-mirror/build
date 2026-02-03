// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"fmt"
	"strings"
)

// Pool is an item in the GN dependency graph that represents a named pool.
//
// A pool is used to limit the parallelism of task invocation in the
// generated ninja build. Pools are referenced by toolchains.
type Pool struct {
	itemInfo
	// The pool depth (number of task to run simultaneously).
	//nolint:unused
	depth int64
}

func (Pool) compatibleWith(item Item) bool {
	switch item.(type) {
	case *Pool:
		return true
	}
	return false
}

// NinjaName returns the pool name in generated ninja files.
func (p Pool) NinjaName(includeToolchain bool) (string, error) {
	var sb strings.Builder
	if includeToolchain {
		if !p.label.ToolchainDir.IsSourceAbsolute() {
			return "", fmt.Errorf("toolchain dir not source absolute: %q", p.label.ToolchainDir.Path())
		}
		for _, r := range p.label.ToolchainDir.Path()[2:] {
			if r == '/' {
				sb.WriteRune('_')
			} else {
				sb.WriteRune(r)
			}
		}
		sb.WriteString(p.label.ToolchainName)
		sb.WriteRune('_')
	}
	if !p.label.Dir.IsSourceAbsolute() {
		return "", fmt.Errorf("label dir not source absolute: %q", p.label.Dir.Path())
	}
	for _, r := range p.label.Dir.Path()[2:] {
		if r == '/' {
			sb.WriteRune('_')
		} else {
			sb.WriteRune(r)
		}
	}
	sb.WriteString(p.label.Name)
	return sb.String(), nil
}
