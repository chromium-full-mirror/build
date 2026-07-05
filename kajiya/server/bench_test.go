// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package server

import (
	"strconv"
	"testing"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// sinkFns keeps benchmark results alive so the returned slice escapes, as it
// does for real callers.
var sinkFns []digest.Function

func BenchmarkAdvertisedDigestFunctions(b *testing.B) {
	cfg := Config{}
	b.ReportAllocs()
	for b.Loop() {
		sinkFns = cfg.AdvertisedDigestFunctions()
	}
}

// BenchmarkResolveRequestDigests measures resolving one request's worth of
// digests the way FindMissingBlobs does: the request-level enum is constant
// across all digests.
func BenchmarkResolveRequestDigests(b *testing.B) {
	cfg := Config{}
	digests := make([]*repb.Digest, 1000)
	for i := range digests {
		digests[i] = digest.SHA256.FromBytes([]byte(strconv.Itoa(i))).Proto()
	}
	b.ReportAllocs()
	for b.Loop() {
		fn, err := cfg.ResolveFunction(repb.DigestFunction_SHA256)
		if err != nil {
			b.Fatal(err)
		}
		for _, d := range digests {
			if _, _, err := cfg.ResolveWith(fn, d); err != nil {
				b.Fatal(err)
			}
		}
	}
}
