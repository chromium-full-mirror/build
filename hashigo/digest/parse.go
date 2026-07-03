// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package digest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/prototext"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// digestPattern matches "hash/size_bytes". The hash must be lowercase hex,
// matching Validate and the REAPI wire format. The accepted hex-hash lengths are
// derived from the registered digest functions (e.g. 32 for MD5/MURMUR3, 40 for
// SHA-1/GITSHA-1, 64 for SHA-256/SHA256TREE/BLAKE3, 66 for VSO, 96 for SHA-384,
// 128 for SHA-512), so the pattern stays in sync as functions are added.
var digestPattern = func() *regexp.Regexp {
	var lens []int
	for _, h := range registry {
		if !slices.Contains(lens, h.hexLen) {
			lens = append(lens, h.hexLen)
		}
	}
	slices.Sort(lens)
	alts := make([]string, len(lens))
	for i, n := range lens {
		alts[i] = fmt.Sprintf("[0-9a-f]{%d}", n)
	}
	return regexp.MustCompile(`^(` + strings.Join(alts, "|") + `)/([0-9]+)$`)
}()

// Parse parses digest string representation.
// It accepts the following string formats.
//   - hash/size_bytes
//   - json representation of digest.
//   - proto text representation of digest.
func Parse(s string) (Digest, error) {
	var d Digest
	m := digestPattern.FindStringSubmatch(s)
	if len(m) == 3 {
		d.Hash = m[1]
		var err error
		d.SizeBytes, err = strconv.ParseInt(m[2], 10, 64)
		if err == nil {
			return d, nil
		}
	}
	// remote-apis-sdks emits "/0" for no digest.
	// e.g. `action_digest:"/0"`
	if s == "/0" {
		return d, nil
	}
	err := json.Unmarshal([]byte(s), &d)
	if err == nil {
		return d, nil
	}
	msg := &rpb.Digest{}
	perr := prototext.Unmarshal([]byte(s), msg)
	if perr == nil {
		d = FromProto(msg)
		return d, nil
	}
	return d, fmt.Errorf("failed to unmarshal %T json:%w proto:%w", msg, err, perr)
}
