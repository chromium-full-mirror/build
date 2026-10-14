// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package actioncache

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
)

// TestUpdateActionResult_RejectsIncompleteRequest checks that
// UpdateActionResult rejects a request without an action result or without
// an action digest with INVALID_ARGUMENT.
func TestUpdateActionResult_RejectsIncompleteRequest(t *testing.T) {
	// Create a service with an empty action cache, and store an action in
	// its CAS so that only the missing field is wrong with each request.
	cas, err := blobstore.NewWithOpts(t.Context(), t.TempDir(), blobstore.Options{})
	if err != nil {
		t.Fatalf("blobstore.NewWithOpts: %v", err)
	}
	ac, err := New(t.Context(), t.TempDir(), cas)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	svc := &Service{ac: ac, cas: cas}
	action, err := proto.Marshal(&repb.Action{})
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	actionDigest, err := cas.Put(digest.SHA256, action)
	if err != nil {
		t.Fatalf("cas.Put: %v", err)
	}

	// Send each incomplete request and check that it is rejected.
	for _, tc := range []struct {
		name string
		req  *repb.UpdateActionResultRequest
	}{
		{
			name: "missing_action_result",
			req:  &repb.UpdateActionResultRequest{ActionDigest: actionDigest.Proto()},
		},
		{
			name: "missing_action_digest",
			req:  &repb.UpdateActionResultRequest{ActionResult: &repb.ActionResult{}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.UpdateActionResult(t.Context(), tc.req)
			if got, want := status.Code(err), codes.InvalidArgument; got != want {
				t.Errorf("UpdateActionResult: code %v (%v), want %v", got, err, want)
			}
		})
	}
}
