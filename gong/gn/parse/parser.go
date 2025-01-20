// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package parse converts GN syntax tokens into an AST.
package parse

import (
	"fmt"

	"infra/build/gong/gn/syntax"
)

type parser struct {
	tokens []syntax.Token
}

// Parse converts a series of tokens into an AST.
func Parse(tokens []syntax.Token) (ParseNode, error) {
	p := parser{tokens}
	return p.parseFile()
}

func (p *parser) parseFile() (ParseNode, error) {
	file := BlockNode{
		ResultMode: DiscardsResult,
	}
	return nil, fmt.Errorf("ParseFile not implemented, file: %v", file)
}
