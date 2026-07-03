// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package digest

import (
	"testing"
)

// testDigestStr123 is the digest string for []byte{1, 2, 3}.
const testDigestStr123 = "039058c6f2c0cb492c533b0a4d14ef77cc0f78abccced5287d84a1a2011cfb81/3"

func TestDigest(t *testing.T) {
	// Regular case
	b := []byte{1, 2, 3}
	d := SHA256.FromBytes(b)

	if d.String() != testDigestStr123 {
		t.Errorf("SHA256.FromBytes(%v).String() = %s, want %s", b, d.String(), testDigestStr123)
	}

	p := d.Proto()
	if p == nil {
		t.Errorf("SHA256.FromBytes(%v).Proto() = nil, want a Digest proto", b)
	}

	dFromProto := FromProto(p)
	if dFromProto != d {
		t.Errorf("FromProto(%v) = %v, want %v", p, dFromProto, d)
	}

	// From nil proto
	nild := FromProto(nil)
	if nild.IsZero() != true {
		t.Errorf("FromProto(nil).IsZero() = false, want true")
	}

	// Empty digest
	empty := SHA256.FromBytes([]byte{})
	if empty.SizeBytes != 0 {
		t.Errorf("SHA256.FromBytes([]byte{}).SizeBytes = %v, want 0", empty.SizeBytes)
	}
	if empty.IsZero() {
		t.Errorf("SHA256.FromBytes([]byte{}).IsZero() = true, want false")
	}
	if empty != SHA256.Empty() {
		t.Errorf("SHA256.FromBytes([]byte{}) = %v, want %v", empty, SHA256.Empty())
	}

	// EmptyTree digest
	if SHA256.EmptyTree().SizeBytes != 2 {
		t.Errorf("SHA256.EmptyTree().SizeBytes = %v, want 2", SHA256.EmptyTree().SizeBytes)
	}
}
