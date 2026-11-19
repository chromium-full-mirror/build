// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"testing"

	"google.golang.org/protobuf/proto"

	pb "go.chromium.org/build/siso/hashfs/proto"
)

func BenchmarkUnmarshalState(b *testing.B) {
	buf, err := proto.Marshal(testState(200000))
	if err != nil {
		b.Fatal(err)
	}
	b.Run("proto.Unmarshal", func(b *testing.B) {
		b.SetBytes(int64(len(buf)))
		for b.Loop() {
			state := &pb.State{}
			if err := proto.Unmarshal(buf, state); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("unmarshalState", func(b *testing.B) {
		b.SetBytes(int64(len(buf)))
		for b.Loop() {
			if _, err := unmarshalState(buf); err != nil {
				b.Fatal(err)
			}
		}
	})
}
