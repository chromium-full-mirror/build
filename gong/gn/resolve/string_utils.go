// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.
package resolve

import (
	"strings"

	"go.chromium.org/build/gong/gn/syntax"
)

func expandStringLiteral(token syntax.Token) (Value, error) {
	if token.TokenType() != syntax.TokenString {
		return nil, token.MakeError(syntax.ErrInvalidOperation, "This is not a string")
	}

	// The parser should have kept the surrounding quotes.
	str := token.Value()
	if len(str) < 2 {
		return nil, token.MakeErrorWithHelp(syntax.ErrInvalidAST, "Invalid AST", "Found a LiteralNode with an unquoted string")
	}
	if str[0] != '"' || str[len(str)-1] != '"' {
		return nil, token.MakeErrorWithHelp(syntax.ErrInvalidAST, "Invalid AST", "Found an incorrectly-quoted LiteralNode")
	}

	// Because the token includes the surrounding quotes, strip those off.
	rawInput := str[1 : len(str)-1]
	finalSize := len(rawInput)

	var output strings.Builder
	output.Grow(finalSize)
	for i := 0; i < finalSize; i++ { // don't use `range` since need ability to fast-forward `i`.
		switch rawInput[i] {
		case '\\':
			if i < finalSize-1 {
				switch rawInput[i+1] {
				case '\\', '"', '$':
					output.WriteByte(rawInput[i+1])
					i++
					continue
				}
			}
			// Everything else has no meaning: pass the literal.
			output.WriteByte(rawInput[i])
		case '$':
			if i+1 == finalSize {
				return nil, token.MakeErrorWithHelp(syntax.ErrInvalidAST, "$ at end of string.", "I was expecting an identifier, 0xFF, or {...} after the $.")
			}
			// TODO(b/388723392): Implement interpolation.
			if rawInput[i+1] == '0' {
				return nil, token.MakeError(syntax.ErrNotImplemented, "appendHexByte not implemented")
			} else {
				return nil, token.MakeError(syntax.ErrNotImplemented, "$identifier interpolation not implemented")
			}
		default:
			output.WriteByte(rawInput[i])
		}
	}
	return &StringValue{
		value: output.String(),
	}, nil
}
