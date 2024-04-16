// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	"golang.org/x/sync/singleflight"

	"infra/build/kajiya/atomicio"
)

// ContentAddressableStorage is a simple CAS implementation that stores files on the local disk.
type ContentAddressableStorage struct {
	dataDir string

	// Synchronization mechanism to prevent concurrent puts of the same blob.
	putSyncer singleflight.Group
}

// New creates a new local CAS. The data directory is created if it does not exist.
func New(dataDir string) (*ContentAddressableStorage, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data directory must be specified")
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	// Create subdirectories {00, 01, ..., ff} for sharding by hash prefix.
	for i := 0; i <= 255; i++ {
		err := os.Mkdir(filepath.Join(dataDir, fmt.Sprintf("%02x", i)), 0755)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return nil, err
		}
	}

	cas := &ContentAddressableStorage{
		dataDir: dataDir,
	}

	// Ensure that we have the "empty blob" present in the CAS.
	// Clients will usually not upload it, but just assume that it's always available.
	// A faster way would be to special case the empty digest in the CAS implementation,
	// but this is simpler and more robust.
	d, err := cas.Put(nil)
	if err != nil {
		return nil, err
	}
	if d != digest.Empty {
		return nil, fmt.Errorf("empty blob did not have expected hash: got %s, wanted %s", d, digest.Empty)
	}

	return cas, nil
}

// path returns the path to the file with digest d in the CAS.
func (c *ContentAddressableStorage) path(d digest.Digest) string {
	return filepath.Join(c.dataDir, d.Hash[:2], d.Hash)
}

// Stat returns os.FileInfo for the requested digest if it exists.
func (c *ContentAddressableStorage) Stat(d digest.Digest) (os.FileInfo, error) {
	p := c.path(d)

	fi, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &MissingBlobsError{Blobs: []digest.Digest{d}}
		}
		return nil, err
	}

	if fi.Size() != d.Size {
		log.Printf("actual file size %d does not match requested size of digest %s", fi.Size(), d.String())
		return nil, &MissingBlobsError{Blobs: []digest.Digest{d}}
	}

	return fi, nil
}

// Has returns true if the requested digest exists in the CAS.
func (c *ContentAddressableStorage) Has(d digest.Digest) bool {
	if _, err := c.Stat(d); err != nil {
		var mbe *MissingBlobsError
		if !errors.As(err, &mbe) {
			// That's unexpected, let's log it.
			log.Printf("error checking for digest %s: %s", d.String(), err)
		}
		return false
	}
	return true
}

// Open returns an io.ReadCloser for the requested digest if it exists.
// The returned ReadCloser is limited to the given offset and limit.
// The offset must be non-negative and no larger than the file size.
// A limit of 0 means no limit, and a limit that's larger than the file size is truncated to the file size.
func (c *ContentAddressableStorage) Open(d digest.Digest, offset int64, limit int64) (io.ReadCloser, error) {
	p := c.path(d)

	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &MissingBlobsError{Blobs: []digest.Digest{d}}
		}
		return nil, err
	}

	// Ensure that the file has the expected size.
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		// Error is safe to ignore, because we're just reading.
		_ = f.Close()
		return nil, err
	}

	if size != d.Size {
		log.Printf("actual file size %d does not match requested size of digest %s", offset, d.String())
		_ = f.Close()
		return nil, &MissingBlobsError{Blobs: []digest.Digest{d}}
	}

	// Ensure that the offset is not negative and not larger than the file size.
	if offset < 0 || offset > size {
		_ = f.Close()
		return nil, fs.ErrInvalid
	}

	// Seek to the requested offset.
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}

	// Cap the limit to the file size, taking the offset into account.
	if limit == 0 || limit > size-offset {
		limit = size - offset
	}

	return LimitReadCloser(f, limit), nil
}

// Get reads a file for the given digest from disk and returns its contents.
func (c *ContentAddressableStorage) Get(d digest.Digest) ([]byte, error) {
	// Just call Open and read the whole file.
	f, err := c.Open(d, 0, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Error is safe to ignore, because we're just reading.
		_ = f.Close()
	}()
	return io.ReadAll(f)
}

// Put stores the given data in the CAS and returns its digest.
func (c *ContentAddressableStorage) Put(data []byte) (digest.Digest, error) {
	d := digest.NewFromBlob(data)
	_, err, _ := c.putSyncer.Do(d.Hash, func() (any, error) {
		// If the file is already in the CAS, we're done.
		if c.Has(d) {
			return nil, nil
		}

		// Add the file to the CAS.
		if err := atomicio.WriteFile(c.path(d), data); err != nil {
			return nil, err
		}
		return nil, nil
	})
	return d, err
}

// Adopt moves a file from the given path into the CAS.
// The digest is assumed to have been validated by the caller.
func (c *ContentAddressableStorage) Adopt(d digest.Digest, srcPath string) error {
	_, err, _ := c.putSyncer.Do(d.Hash, func() (any, error) {
		// If the file is already in the CAS, we're done.
		if c.Has(d) {
			if err := os.Remove(srcPath); err != nil {
				return nil, err
			}
			return nil, nil
		}

		// Move the file into the CAS.
		if err := os.Rename(srcPath, c.path(d)); err != nil {
			return nil, err
		}
		return nil, nil
	})
	return err
}

// LinkTo creates a link `path` pointing to the file with digest `d` in the CAS.
// If the operating system supports cloning files via copy-on-write semantics,
// the file is cloned instead of hard linked.
func (c *ContentAddressableStorage) LinkTo(d digest.Digest, path string) error {
	if err := FastCopy(c.path(d), path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &MissingBlobsError{Blobs: []digest.Digest{d}}
		}
		return err
	}
	return nil
}
