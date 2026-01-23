// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.
package resolve

import (
	"strconv"
	"strings"

	"go.chromium.org/build/gong/gn/syntax"
)

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')
}

// appendHexByte handles a hex literal: $0xFF
//
// i is the index into input after the $.
//
// On failure, returns error. On success, appends the char with the given
// hex value to output and returns the index pointing to the last character consumed.
func appendHexByte(token syntax.Token, input string, i int, output *strings.Builder) (int, error) {
	// "$0" is already known to exist.
	if i+4 > len(input) || input[i+1] != 'x' || !isHex(input[i+2]) || !isHex(input[i+3]) {
		return 0, token.MakeError(syntax.ErrInvalidFormat, "Invalid hex character. Hex values must look like 0xFF.")
	}
	val, err := strconv.ParseUint(input[i+2:i+4], 16, 8)
	if err != nil {
		return 0, token.MakeError(syntax.ErrInvalidFormat, "Could not convert hex value.")
	}
	output.WriteByte(byte(val))
	return i + 3, nil
}

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
			i++
			if i == finalSize {
				return nil, token.MakeErrorWithHelp(syntax.ErrInvalidAST, "$ at end of string.", "I was expecting an identifier, 0xFF, or {...} after the $.")
			}
			if rawInput[i] == '0' {
				var err error
				i, err = appendHexByte(token, rawInput, i, &output)
				if err != nil {
					return nil, err
				}
			} else {
				// TODO(b/388723392): Implement interpolation.
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
