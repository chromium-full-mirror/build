// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.
package resolve

import (
	"fmt"
	"strconv"
	"strings"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')
}

func isIdentifierFirstChar(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_'
}

func isIdentifierContinuingChar(c byte) bool {
	return isIdentifierFirstChar(c) || (c >= '0' && c <= '9')
}

// appendHexByte handles a hex literal: $0xFF
//
// i is the index into input after the $.
//
// On failure, returns error. On success, appends the char with the given
// hex value to output and returns the index pointing to the last character consumed.
func appendHexByte(originNode parse.Node, input string, i int, output *strings.Builder) (int, error) {
	// "$0" is already known to exist.
	if i+4 > len(input) || input[i+1] != 'x' || !isHex(input[i+2]) || !isHex(input[i+3]) {
		return 0, StringLiteralError{
			OriginNode: parse.OriginNode{Node: originNode},
			message:    "Invalid hex character. Hex values must look like 0xFF.",
		}
	}
	val, err := strconv.ParseUint(input[i+2:i+4], 16, 8)
	if err != nil {
		return 0, StringLiteralError{
			OriginNode: parse.OriginNode{Node: originNode},
			message:    "Could not convert hex value.",
		}
	}
	output.WriteByte(byte(val))
	return i + 3, nil
}

func expandStringLiteral(token syntax.Token, originNode parse.Node, scope *Scope) (Value, error) {
	if token.TokenType() != syntax.TokenString {
		return nil, TypeError{
			Msg:              "This is not a string",
			locationOverride: originNode.LocationRange().Begin(),
			rangesOverride:   []syntax.LocationRange{originNode.LocationRange()},
		}
	}

	// The parser should have kept the surrounding quotes.
	str := token.Value()
	if len(str) < 2 {
		return nil, ASTError{
			OriginNode: parse.OriginNode{Node: originNode},
			details:    "Received a LiteralNode with an unquoted string",
		}
	}
	if str[0] != '"' || str[len(str)-1] != '"' {
		return nil, ASTError{
			OriginNode: parse.OriginNode{Node: originNode},
			details:    "Received an incorrectly-quoted LiteralNode",
		}
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
				return nil, StringLiteralError{
					OriginNode: parse.OriginNode{Node: originNode},
					message:    "$ at end of string.",
					helpText:   "I was expecting an identifier, 0xFF, or {...} after the $.",
				}
			}
			var err error
			switch rawInput[i] {
			case '0': // $0...
				i, err = appendHexByte(originNode, rawInput, i, &output)
				if err != nil {
					return nil, err
				}
			case '{': // ${...
				i++
				interpStart := i
				hasNonIdentChars := false
				for i < finalSize && rawInput[i] != '}' {
					if !isIdentifierContinuingChar(rawInput[i]) {
						hasNonIdentChars = true
					}
					i++
				}
				if i == len(rawInput) {
					return nil, StringLiteralError{
						OriginNode: parse.OriginNode{Node: originNode},
						message:    "Unterminated ${...",
					}
				}
				if !hasNonIdentChars {
					// Prefer to treat as $foo where possible, so don't need to execute parser.
					err = appendInterpolatedIdentifier(scope, originNode, rawInput[interpStart:i], &output)
				} else {
					// Can't treat as simple identifier, must execute parser.
					err = appendInterpolatedExpression(scope, token, originNode, rawInput[interpStart:i], &output)
				}
				if err != nil {
					return nil, err
				}
			default: // $foo...
				if !isIdentifierFirstChar(rawInput[i]) {
					return nil, StringLiteralError{
						OriginNode: parse.OriginNode{Node: originNode},
						message:    "$ not followed by an identifier char.",
						helpText:   `If you want a literal $ use "\$".`,
					}
				}
				// Find the first non-identifier char following the string.
				interpStart := i
				i++
				for i < len(rawInput) && isIdentifierContinuingChar(rawInput[i]) {
					i++
				}
				interpEnd := i
				err = appendInterpolatedIdentifier(scope, originNode, rawInput[interpStart:interpEnd], &output)
				if err != nil {
					return nil, err
				}
				i-- // At end of interpolation, go back one char for next loop to iterate i++.
			}
		default:
			output.WriteByte(rawInput[i])
		}
	}
	return &StringValue{
		value: output.String(),
	}, nil
}

func appendInterpolatedIdentifier(scope *Scope, originNode parse.Node, identifier string, output *strings.Builder) error {
	val := scope.Value(identifier, true)
	if val == nil {
		return StringLiteralError{
			OriginNode: parse.OriginNode{Node: originNode},
			message:    "Undefined identifier in string expansion.",
			helpText:   fmt.Sprintf("%q is not currently in scope.", identifier),
		}
	}
	output.WriteString(val.RawGNString())
	return nil
}

func appendInterpolatedExpression(scope *Scope, token syntax.Token, originNode parse.Node, exprStr string, output *strings.Builder) error {
	tokens, err := syntax.Tokenize(syntax.LiteralInput{Bytes: []byte(exprStr)})
	if err != nil {
		return StringLiteralExpressionError{
			OriginToken: syntax.OriginToken{Token: token},
			err:         err,
		}
	}
	node, err := parse.ParseExpression(tokens)
	if err != nil {
		return StringLiteralExpressionError{
			OriginToken: syntax.OriginToken{Token: token},
			err:         err,
		}
	}

	// Interpolated expression only allows identifiers and accessors (e.g., a.b, a[0])
	switch node.(type) {
	case *parse.IdentifierNode, *parse.AccessorNode:
		// OK
	default:
		return StringLiteralError{
			OriginNode: parse.OriginNode{Node: originNode},
			message:    "Invalid string interpolation.",
			helpText: `The thing inside the ${} must be an identifier ${foo},
a scope access ${foo.bar}, or a list access ${foo[0]}.`,
		}
	}

	result, err := ExecuteNode(node, scope)
	if err != nil {
		return err
	}

	output.WriteString(result.RawGNString())
	return nil
}
