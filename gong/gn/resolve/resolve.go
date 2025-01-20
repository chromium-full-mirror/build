// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package resolve provides an environment for executing a GN AST.
package resolve

import (
	"fmt"

	"infra/build/gong/gn/parse"
)

// ExecuteNode executes a given node in the AST.
func ExecuteNode(n parse.ParseNode) (Value, error) {
	return nil, fmt.Errorf("don't know how to execute nodes yet")
}
