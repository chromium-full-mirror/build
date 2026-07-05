// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"go.chromium.org/build/hashigo/digest"
)

// DigestMismatchError represents an error where the actual digest differs from the expected digest during verification.
type DigestMismatchError struct {
	Actual   digest.Digest
	Expected digest.Digest
}

// Error returns a human-readable error message.
func (e *DigestMismatchError) Error() string {
	return fmt.Sprintf("hash mismatch: got %s, want %s", e.Actual, e.Expected)
}

// UploadTooLargeError represents an error where the uploaded blob is larger than the expected size.
type UploadTooLargeError struct {
	Actual   int64
	Expected int64
}

// Error returns a human-readable error message.
func (e *UploadTooLargeError) Error() string {
	return fmt.Sprintf("upload too large: got %d bytes, want %d bytes", e.Actual, e.Expected)
}

// ErrBlobExists is returned by NewUploadWriter if the CAS already contains the expected digest.
var ErrBlobExists = errors.New("blob already exists")

// UploadWriter is an io.WriteCloser that will add the written data as a new blob to the CAS if the calculated digest
// matches the given digest upon calling Close().
type UploadWriter struct {
	fn             digest.Function
	expectedDigest digest.Digest
	uploadID       uuid.UUID

	cas    *ContentAddressableStorage
	file   *os.File
	path   string
	hasher hash.Hash
	size   int64
}

// NewUploadWriter creates a new UploadWriter for the given digest. If the CAS already contains the digest,
// NewUploadWriter will return ErrBlobExists.
func (c *ContentAddressableStorage) NewUploadWriter(fn digest.Function, d digest.Digest, uploadID uuid.UUID) (*UploadWriter, error) {
	if c.Has(fn, d) {
		return nil, ErrBlobExists
	}

	// Build the hasher for the blob's digest function. For git-framing
	// functions this seeds the "blob <size>\0" header, so the streamed Write
	// calls only contribute raw content.
	hasher := fn.NewContentHasher(d.SizeBytes)

	path := filepath.Join(c.tmpDir, uploadID.String())
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return nil, err
	}

	return &UploadWriter{
		fn:             fn,
		expectedDigest: d,
		uploadID:       uploadID,
		cas:            c,
		file:           f,
		path:           path,
		hasher:         hasher,
	}, nil
}

// Write writes the given data to the file and updates the hash and size.
// If the CAS already contains the expected digest, Write will return ErrBlobExists.
func (uw *UploadWriter) Write(p []byte) (n int, err error) {
	if uw.file == nil {
		return 0, fs.ErrInvalid
	}

	// Delete the temporary file if we run into an error.
	defer func() {
		if err != nil {
			if e := uw.file.Close(); e != nil {
				slog.Warn("failed to close temporary file", "path", uw.path, "error", e)
			}
			if e := os.Remove(uw.path); e != nil {
				slog.Warn("failed to remove temporary file", "path", uw.path, "error", e)
			}
			uw.file = nil
		}
	}()

	if uw.cas.Has(uw.fn, uw.expectedDigest) {
		return 0, ErrBlobExists
	}

	n, err = uw.file.Write(p)
	if n > 0 {
		uw.hasher.Write(p[:n])
		uw.size += int64(n)
	}
	if err == nil && uw.size > uw.expectedDigest.SizeBytes {
		err = &UploadTooLargeError{Actual: uw.size, Expected: uw.expectedDigest.SizeBytes}
	}

	return n, err
}

// Close finishes the upload, verifies that the digest matches the expected value, and then moves the temporary file
// into the CAS.
func (uw *UploadWriter) Close() (err error) {
	// Ensure that the writer can only be closed once.
	if uw.file == nil {
		return fs.ErrInvalid
	}

	// Make sure that we delete the temporary file if we run into an error.
	defer func() {
		if err != nil {
			if e := os.Remove(uw.path); e != nil {
				slog.Warn("failed to remove temporary file", "path", uw.path, "error", e)
			}
		}
	}()

	if err = uw.file.Close(); err != nil {
		return err
	}
	uw.file = nil

	if d := uw.digest(); d != uw.expectedDigest {
		return &DigestMismatchError{Actual: d, Expected: uw.expectedDigest}
	}

	if err = uw.cas.Adopt(uw.fn, uw.expectedDigest, uw.path); err != nil {
		return err
	}

	return nil
}

// digest returns the digest of the data that has been written so far.
func (uw *UploadWriter) digest() digest.Digest {
	return digest.Digest{
		Hash:      hex.EncodeToString(uw.hasher.Sum(nil)),
		SizeBytes: uw.size,
	}
}
