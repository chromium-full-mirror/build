// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/atomicio"
)

// splitPath returns the path to the file storing the split mapping for the blob with digest d in the CAS.
func (c *ContentAddressableStorage) splitPath(fn digest.Function, d digest.Digest) string {
	dir, ok := c.splitRoots[fn]
	if !ok {
		dir = filepath.Join(c.dataDir, "splits", fn.String())
	}
	if c.sharded {
		return filepath.Join(dir, d.Hash[:2], d.Hash)
	}
	return filepath.Join(dir, d.Hash)
}

// Split returns the stored chunk split mapping for the given blob digest if available.
// Also checks that the blob itself and all referenced chunks still exist in the CAS.
// No On-The-Fly Chunking: When SplitBlob is requested for a blob
// that exists in CAS but was uploaded without chunking information
// (e.g. via normal ByteStream.Write), Kajiya will return codes.NotFound
// as allowed by the REAPI specification, rather than performing
// computationally expensive content-defined chunking (CDC) on demand.
// TODO: support on-the-fly chunking?
func (c *ContentAddressableStorage) Split(fn digest.Function, d digest.Digest) (*repb.SplitBlobResponse, error) {
	if !c.Has(fn, d) {
		return nil, &MissingBlobsError{Fn: fn, Blobs: []digest.Digest{d}}
	}

	p := c.splitPath(fn, d)
	buf, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &MissingSplitError{Fn: fn, Blob: d}
		}
		return nil, err
	}

	resp := &repb.SplitBlobResponse{}
	if err := proto.Unmarshal(buf, resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal split mapping: %w", err)
	}

	// Check that all chunk blobs are still present in the CAS.
	var missing []digest.Digest
	for _, chunk := range resp.ChunkDigests {
		cd, err := fn.FromProto(chunk)
		if err != nil {
			return nil, fmt.Errorf("invalid chunk digest in split mapping: %w", err)
		}
		if !c.Has(fn, cd) {
			missing = append(missing, cd)
		}
	}
	if len(missing) > 0 {
		slices.SortFunc(missing, func(a, b digest.Digest) int { return strings.Compare(a.Hash, b.Hash) })
		missing = slices.Compact(missing)
		return nil, &MissingBlobsError{Fn: fn, Blobs: missing}
	}
	// TODO: touch blob and digest to extend lifetime?
	return resp, nil
}

// Splice registers a blob as the concatenation of the given chunk digests in the CAS.
// It verifies that all chunks exist in the CAS and that their concatenation matches the expected blob digest.
func (c *ContentAddressableStorage) Splice(fn digest.Function, blobDigest digest.Digest, chunkDigests []digest.Digest, chunkingFunc repb.ChunkingFunction_Value) error {
	var totalSize int64
	for _, cd := range chunkDigests {
		totalSize += cd.SizeBytes
	}
	if totalSize != blobDigest.SizeBytes {
		return &DigestMismatchError{
			Actual: digest.Digest{
				SizeBytes: totalSize,
			},
			Expected: blobDigest,
		}
	}
	_, err, _ := c.putSyncer.Do("splice/"+DigestKey(fn, blobDigest), func() (any, error) {
		// 1. Verify all chunk blobs exist in CAS.
		var missing []digest.Digest
		for _, cd := range chunkDigests {
			if !c.Has(fn, cd) {
				missing = append(missing, cd)
			}
		}
		if len(missing) > 0 {
			slices.SortFunc(missing, func(a, b digest.Digest) int { return strings.Compare(a.Hash, b.Hash) })
			missing = slices.Compact(missing)
			return nil, &MissingBlobsError{Fn: fn, Blobs: missing}
		}

		// 2. Ensure the full blob exists in CAS, verifying the concatenated contents along the way.
		// TODO: not create concatenated large blob.
		// bytestream etc should read splits and concatenate on-the-fly.
		// TODO: verify chunk is created using chunking function?
		uw, err := c.NewUploadWriter(fn, blobDigest, uuid.New())
		if err == nil {
			for _, cd := range chunkDigests {
				rc, err := c.Open(fn, cd, 0, 0)
				if err != nil {
					_ = uw.Close()
					return nil, err
				}
				_, err = io.Copy(uw, rc)
				_ = rc.Close()
				if err != nil {
					_ = uw.Close()
					return nil, err
				}
			}
			if err := uw.Close(); err != nil {
				return nil, err
			}
		} else if errors.Is(err, ErrBlobExists) {
			// Even if the blob already exists in CAS, verify that concatenating the chunks produces the expected digest.
			hasher := fn.NewContentHasher(blobDigest.SizeBytes)
			var size int64
			for _, cd := range chunkDigests {
				rc, err := c.Open(fn, cd, 0, 0)
				if err != nil {
					return nil, err
				}
				n, err := io.Copy(hasher, rc)
				_ = rc.Close()
				if err != nil {
					return nil, err
				}
				size += n
			}
			actual := digest.Digest{
				Hash:      hex.EncodeToString(hasher.Sum(nil)),
				SizeBytes: size,
			}
			if actual != blobDigest {
				return nil, &DigestMismatchError{Actual: actual, Expected: blobDigest}
			}
		} else {
			return nil, err
		}

		// 3. Save the split mapping to disk.
		resp := &repb.SplitBlobResponse{
			ChunkDigests:     make([]*repb.Digest, len(chunkDigests)),
			ChunkingFunction: chunkingFunc,
		}
		for i, cd := range chunkDigests {
			resp.ChunkDigests[i] = cd.Proto()
		}

		mappingBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(resp)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal split mapping: %w", err)
		}

		if err := atomicio.WriteFile(c.splitPath(fn, blobDigest), mappingBytes); err != nil {
			return nil, fmt.Errorf("failed to save split mapping: %w", err)
		}

		return nil, nil
	})
	return err
}
