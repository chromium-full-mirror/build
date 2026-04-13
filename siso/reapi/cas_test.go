// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package reapi

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/siso/reapi/digest"
)

func TestCreateBatchUpdateBlobsRequests(t *testing.T) {
	ctx := t.Context()
	rnd := rand.NewChaCha8([32]byte{})
	ds := digest.NewStore()
	testdata := func(s string, n int64) digest.Data {
		buf := make([]byte, n)
		rnd.Read(buf)
		return digest.FromBytes(s, buf)
	}
	ds.Set(testdata("data 0", 512*1024))
	for i := 1; i < 15; i++ {
		ds.Set(testdata(fmt.Sprintf("data %d", i), 1024*1024))
	}
	uploadOps := make(map[digest.Digest]*uploadOp)
	for _, d := range ds.List() {
		uploadOps[d] = newUploadOp()
	}
	sizeLimit := int64(10 * 1024 * 1024)
	numLimit := 10
	blobsReqs, missingBlobs := blobsToUpload(ctx, ds.List(), ds, sizeLimit)
	batchReqs := createBatchUpdateBlobsRequests("projects/test/instances/default_instannce", blobsReqs, sizeLimit, numLimit)
	nBatches := 0
	for batchReq := range batchReqs {
		size := int64(proto.Size(batchReq))
		num := len(batchReq.Requests)
		t.Logf("size=%d num=%d", size, num)
		if size > sizeLimit || num > numLimit {
			t.Errorf("size=%d num=%d; exceeds limit size=%d num=%d", size, num, sizeLimit, numLimit)
		}
		nBatches++
	}
	if nBatches != 2 {
		t.Errorf("too many batch requests %d; want 2", nBatches)
	}
	if m := missingBlobs.Size(); m != 0 {
		t.Errorf("missingBlobs=%d; want=0", m)
	}
}
