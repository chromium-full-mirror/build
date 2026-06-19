// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"crypto/sha256"
	"encoding/hex"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type stringHashFunction struct{}

func (stringHashFunction) HelpShort() string {
	return "string_hash: Calculates a stable hash of the given string."
}

func (stringHashFunction) Help() string {
	return `string_hash: Calculates a stable hash of the given string.

  hash = string_hash(long_string)

` + "`string_hash` returns a string that contains a hash of the argument.  The hash" + `
  is computed by first calculating the SHA256 hash of the argument, and then
  returning the first 8 characters of the lowercase-ASCII, hexadecimal encoding
  of the SHA256 hash.

` + "`string_hash` is intended to be used when it is desirable to translate," + `
  globally unique strings (such as GN labels) into short filenames that are
  still globally unique.  This is useful when supporting filesystems and build
  systems which impose limits on the length of the supported filenames and/or on
  the total path length.

  Warning: This hash should never be used for cryptographic purposes.
  Unique inputs can be assumed to result in unique hashes if the inputs
  are trustworthy, but malicious inputs may be able to trigger collisions.
  Directories and names of GN labels are usually considered trustworthy.

Examples:

    string_hash("abc")  -->  "ba7816bf"
`
}

func (stringHashFunction) IsTarget() bool { return false }

func (stringHashFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value) (resolve.Value, error) {
	// Check usage: Number of arguments.
	if len(args) != 1 {
		return nil, resolve.ArgumentCountError{
			OriginFunction: resolve.OriginFunction{Call: call},
			Msg:            "Wrong number of arguments to string_hash().",
			Help:           "Expecting exactly one. usage: string_hash(string)",
		}
	}

	// Check usage: argument is a string.
	v, err := resolve.AsValue[*resolve.StringValue](args[0])
	if err != nil {
		return nil, resolve.TypeError{
			Value: args[0],
			Msg:   "argument of string_hash is not a string",
			Help:  "Expecting argument to be a string.",
		}
	}

	// Arguments looks good; do the hash.
	hash := sha256.Sum256([]byte(v.RawGNString()))
	hashStr := hex.EncodeToString(hash[:])

	// Trimming to 32 bits for improved ergonomics.  Probability of collisions
	// should still be sufficiently low (see https://crbug.com/46330294 for more
	// discussion).
	return resolve.NewStringValueAt(call, hashStr[:8]), nil
}
