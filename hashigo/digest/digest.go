// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package digest handles content digests of remote executon API.
//
// You can find the Digest proto in REAPI here:
// https://github.com/bazelbuild/remote-apis/blob/c1c1ad2c97ed18943adb55f06657440daa60d833/build/bazel/remote/execution/v2/remote_execution.proto#L633
package digest

import (
	"fmt"
	"sync"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

var copyBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 32*1024)
		return &buf
	},
}

// Digest is a content digest: a hash and the content size in bytes.
//
// It does not record which digest function produced the hash; that is conveyed
// out-of-band (as in the REAPI Digest message) by the Function held by the
// caller. Digest is a pure comparable value, safe as a map key.
type Digest struct {
	Hash      string `json:"hash,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// FromProto converts from digest proto.
func FromProto(d *rpb.Digest) Digest {
	if d == nil {
		return Digest{}
	}
	return Digest{
		Hash:      d.Hash,
		SizeBytes: d.SizeBytes,
	}
}

// IsZero returns true when digest is zero value (equivalent with nil digest proto).
func (d Digest) IsZero() bool {
	return d.Hash == ""
}

// Proto returns digest proto.
func (d Digest) Proto() *rpb.Digest {
	if d.IsZero() {
		return nil
	}
	return &rpb.Digest{
		Hash:      d.Hash,
		SizeBytes: d.SizeBytes,
	}
}

// String returns string representation of the digest (hash/sizes_bytes).
func (d Digest) String() string {
	if d.IsZero() {
		return ""
	}
	return fmt.Sprintf("%s/%d", d.Hash, d.SizeBytes)
}
