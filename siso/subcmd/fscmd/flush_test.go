// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package fscmd

import (
	"strings"
	"testing"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	pb "go.chromium.org/build/siso/hashfs/proto"
)

// checkStateDigestFunction must reject a state recorded under a different
// digest function than the client's: those blobs are unfetchable from CAS by
// this client, so flush would otherwise report every file as "not found".
func TestCheckStateDigestFunction(t *testing.T) {
	blake3, err := digest.Lookup(rpb.DigestFunction_BLAKE3)
	if err != nil {
		t.Fatal(err)
	}
	entries := []*pb.Entry{{Name: "foo"}}
	for _, tc := range []struct {
		name     string
		st       *pb.State
		clientFn digest.Function
		wantErr  bool
	}{
		{
			name:     "match",
			st:       &pb.State{DigestFunction: int32(rpb.DigestFunction_SHA256), Entries: entries},
			clientFn: digest.SHA256,
		},
		{
			name:     "legacy_unset_means_sha256",
			st:       &pb.State{Entries: entries},
			clientFn: digest.SHA256,
		},
		{
			name:     "match_blake3",
			st:       &pb.State{DigestFunction: int32(rpb.DigestFunction_BLAKE3), Entries: entries},
			clientFn: blake3,
		},
		{
			name:     "mismatch",
			st:       &pb.State{DigestFunction: int32(rpb.DigestFunction_BLAKE3), Entries: entries},
			clientFn: digest.SHA256,
			wantErr:  true,
		},
		{
			name:     "empty_state_has_nothing_to_flush",
			st:       &pb.State{DigestFunction: int32(rpb.DigestFunction_BLAKE3)},
			clientFn: digest.SHA256,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkStateDigestFunction(tc.st, tc.clientFn)
			if got, want := err != nil, tc.wantErr; got != want {
				t.Errorf("checkStateDigestFunction(...)=%v; want err=%t", err, want)
			}
		})
	}

	// The error must name both functions and the flag to pass.
	err = checkStateDigestFunction(&pb.State{DigestFunction: int32(rpb.DigestFunction_BLAKE3), Entries: entries}, digest.SHA256)
	if err == nil {
		t.Fatal("checkStateDigestFunction(mismatch)=nil; want error")
	}
	for _, want := range []string{"blake3", "sha256", "-reapi_digest_function=blake3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("checkStateDigestFunction(mismatch)=%q; want it to contain %q", err, want)
		}
	}
}

func TestIsDirEnt(t *testing.T) {
	for _, tc := range []struct {
		name string
		ent  *pb.Entry
		want bool
	}{
		{name: "file", ent: &pb.Entry{Digest: &pb.Digest{Hash: "abc", SizeBytes: 3}}, want: false},
		{name: "file_without_digest", ent: &pb.Entry{Kind: pb.Entry_KIND_FILE, Size: 3}, want: false},
		{name: "symlink", ent: &pb.Entry{Target: "foo"}, want: false},
		{name: "legacy_dir", ent: &pb.Entry{CmdHash: []byte("cmd")}, want: true},
		{name: "dir", ent: &pb.Entry{Kind: pb.Entry_KIND_DIR}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDirEnt(tc.ent); got != tc.want {
				t.Errorf("isDirEnt(%v) = %t; want %t", tc.ent, got, tc.want)
			}
		})
	}
}
