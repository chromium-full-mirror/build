// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package server provides shared configuration for all Kajiya services.
package server

import (
	"strings"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// Config holds shared configuration for all Kajiya services.
type Config struct {
	MaxBatchTotalSizeBytes int64
	MaxRecvMsgSize         int

	// DigestFunctions is the set of digest functions the server advertises
	// and accepts, typically produced by ParseDigestFunctions. Empty means
	// SHA-256 only.
	DigestFunctions []digest.Function

	// for chunked blob support
	EnableChunkedBlobs bool
	FastCDC_2020Params *repb.FastCdc2020Params
	RepMaxCDCParams    *repb.RepMaxCdcParams
}

// RecommendedMaxRecvMsgSize returns the maximum gRPC receive message size
// needed to accommodate the configured MaxBatchTotalSizeBytes. It computes the
// exact worst-case protobuf-encoded message size for a single blob batch,
// accounting for zstd compression overhead on non-compressible data, protobuf
// field encoding overhead, and the message nesting structure.
//
// The gRPC default of 4 MiB is used as a minimum.
func (c Config) RecommendedMaxRecvMsgSize() int {
	const defaultSize = 4 * 1024 * 1024 // 4 MiB (gRPC default)
	if c.MaxBatchTotalSizeBytes == 0 {
		return defaultSize
	}

	// Calculate the worst-case ZSTD compressed size for non-compressible
	// data. We use default encoder settings matching the server's encoder
	// pool.
	enc, _ := zstd.NewWriter(nil)
	defer enc.Close()
	maxDataSize := int64(enc.MaxEncodedSize(int(c.MaxBatchTotalSizeBytes)))
	dummyData := make([]byte, maxDataSize)

	// MaxBatchTotalSizeBytes specifies the maximum uncompressed total size
	// of all blobs in a BatchUpdateBlobsRequest. We should make sure that
	// our MaxRecvMsgSize is at least large enough to accept a single blob
	// of that size.
	// Because the size is specified as the *uncompressed* size, we need to
	// account for the worst possible overhead of zstd-compression in case
	// the data is non-compressible.
	// Note that if the client tries to upload multiple blobs with a total
	// size of MaxBatchTotalSizeBytes the upload will likely fail with a
	// RESOURCE_EXHAUSTED error, but it's impossible to set a reasonable
	// MaxRecvMsgSize to avoid this error - we would have to assume that the
	// client would upload MaxBatchTotalSizeBytes blobs of 1 byte each, which
	// would require us to allow a huge MaxRecvMsgSize.
	// As it's not possible to negotiate a maximum total *number* of blobs
	// per BatchUpdateBlobsRequest in REAPI, nor query the MaxRecvMsgSize of
	// a server, this is an unsolvable problem. The best the client can do is
	// to calculate a MaxSendMsgSize using the same algorithm as we use here,
	// and / or correctly handle RESOURCE_EXHAUSTED errors for batch uploads.
	//
	// The worst-case digest is the advertised function with the longest hex
	// hash (e.g. SHA-512's 128 chars), since its digests make the request
	// proto the largest.
	fn := digest.SHA256
	for _, f := range c.AdvertisedDigestFunctions() {
		if f.HexLen() > fn.HexLen() {
			fn = f
		}
	}
	batchUpdateSize := proto.Size(&repb.BatchUpdateBlobsRequest{
		Requests: []*repb.BatchUpdateBlobsRequest_Request{
			{
				Digest: &repb.Digest{
					Hash:      strings.Repeat("f", fn.HexLen()),
					SizeBytes: maxDataSize,
				},
				Data:       dummyData,
				Compressor: repb.Compressor_ZSTD,
			},
		},
		DigestFunction: fn.Value(),
	})

	return max(defaultSize, batchUpdateSize)
}
