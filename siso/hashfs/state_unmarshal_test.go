// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"fmt"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "go.chromium.org/build/siso/hashfs/proto"
)

// testState returns a state with n entries shaped like the entries of an
// Android or Chromium build: sources with digests, and generated files with
// command hashes. It is shared by the test and the benchmark.
func testState(n int) *pb.State {
	state := &pb.State{DigestFunction: 1}
	for i := range n {
		ent := &pb.Entry{
			Id:          &pb.FileID{ModTime: 1700000000000000000 + int64(i)},
			Name:        fmt.Sprintf("/home/user/src/out/soong/.intermediates/frameworks/base/dir%04d/android_common/file%07d.jar", i%5000, i),
			Digest:      &pb.Digest{Hash: fmt.Sprintf("%064x", i), SizeBytes: int64(i % 100000)},
			UpdatedTime: 1700000000000000000 + int64(i),
		}
		if i%2 == 0 {
			ent.CmdHash = []byte(fmt.Sprintf("%032d", i))
			ent.Local = true
		}
		if i%13 == 0 {
			ent.Target = "target"
		}
		state.Entries = append(state.Entries, ent)
	}
	return state
}

func TestUnmarshalState(t *testing.T) {
	if got := (&pb.State{}).ProtoReflect().Descriptor().Fields().ByName("entries").Number(); got != entriesFieldTag {
		t.Fatalf("State.entries field number = %d, want %d", got, entriesFieldTag)
	}

	full := testState(1000)
	full.LastChecked = "token"
	full.BuildTargets = &pb.BuildTargets{Targets: []string{"all"}}
	full.MissingOutputs = []string{"out/missing"}
	full.MissingDigests = []string{"src/nodigest"}

	for _, tc := range []struct {
		name  string
		state *pb.State
	}{
		{"empty", &pb.State{}},
		{"no entries", &pb.State{LastChecked: "x"}},
		{"full", full},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := proto.Marshal(tc.state)
			if err != nil {
				t.Fatalf("proto.Marshal: %v", err)
			}
			want := &pb.State{}
			if err := proto.Unmarshal(b, want); err != nil {
				t.Fatalf("proto.Unmarshal: %v", err)
			}
			got, err := unmarshalState(b)
			if err != nil {
				t.Fatalf("unmarshalState: %v", err)
			}
			if !proto.Equal(got, want) {
				t.Errorf("unmarshalState differs from proto.Unmarshal")
			}
		})
	}
}

func TestUnmarshalState_TruncatedEntry(t *testing.T) {
	if _, err := unmarshalState([]byte{0x0a, 0x05, 0x01}); err == nil {
		t.Errorf("unmarshalState succeeded; want error")
	}
}
