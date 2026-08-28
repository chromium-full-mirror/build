// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/kajiya/atomicio"
)

// ContentAddressableStorage is a simple CAS implementation that stores files on the local disk.
type ContentAddressableStorage struct {
	dataDir string
	tmpDir  string
	sharded bool

	// Configured digest functions and their precomputed root directories
	// (<function>/ under the data dir).
	fns        []digest.Function
	roots      map[digest.Function]string
	splitRoots map[digest.Function]string

	// Synchronization mechanism to prevent concurrent puts of the same blob.
	putSyncer singleflight.Group
}

// Options configures a local CAS.
type Options struct {
	// Sharded enables a two-level on-disk layout where blobs are placed under
	// subdirectories named by the first byte of their hash ({00, 01, ..., ff}).
	// Recommended for production where the CAS may hold many blobs; the
	// zero value (no sharding) avoids 256 mkdir calls and is cheaper for
	// short-lived caches (e.g. in tests).
	Sharded bool

	// SkipValidation skips re-hashing all existing blobs on startup.
	SkipValidation bool

	// DigestFunctions is the set of digest functions to provision storage
	// roots for and validate on startup, typically the server's advertised
	// set. Data under other functions' roots is left untouched and ignored.
	// Empty means SHA-256 only, matching the server's default.
	DigestFunctions []digest.Function
}

// New creates a new local CAS with default options. The data directory is created if it does not exist.
func New(ctx context.Context, dataDir string) (*ContentAddressableStorage, error) {
	return NewWithOpts(ctx, dataDir, Options{})
}

// NewWithOpts creates a new local CAS. The data directory is created if it does not exist.
func NewWithOpts(ctx context.Context, dataDir string, opts Options) (*ContentAddressableStorage, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data directory must be specified")
	}

	if err := os.Mkdir(dataDir, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}

	// Ensure that each configured digest function's root (<function>/ under
	// the data dir) has the correct layout.
	fns := opts.DigestFunctions
	if len(fns) == 0 {
		fns = []digest.Function{digest.SHA256}
	}
	roots, err := EnsureFunctionRoots(dataDir, fns, opts.Sharded)
	if err != nil {
		return nil, fmt.Errorf("provisioning CAS storage roots: %w", err)
	}
	splitsDir := filepath.Join(dataDir, "splits")
	if err := os.Mkdir(splitsDir, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	splitRoots, err := EnsureFunctionRoots(splitsDir, fns, opts.Sharded)
	if err != nil {
		return nil, fmt.Errorf("provisioning CAS split storage roots: %w", err)
	}

	// Wipe any leftover upload temp files from a previous run that may have crashed mid-upload.
	tmpDir := filepath.Join(dataDir, "tmp")
	if err := os.RemoveAll(tmpDir); err != nil {
		slog.Warn("failed to delete temp dir", "path", tmpDir, "error", err)
	}
	if err := os.Mkdir(tmpDir, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}

	cas := &ContentAddressableStorage{
		dataDir:    dataDir,
		tmpDir:     tmpDir,
		sharded:    opts.Sharded,
		fns:        fns,
		roots:      roots,
		splitRoots: splitRoots,
	}

	// Ensure that the "empty blob" is present in the CAS for every configured
	// digest function: clients will usually not upload it, but just assume
	// that it's always available.
	for _, fn := range fns {
		want := fn.Empty()
		d, err := cas.Put(fn, nil)
		if err != nil {
			return nil, err
		}
		if d != want {
			return nil, fmt.Errorf("empty blob did not have expected hash: got %s, wanted %s", d, want)
		}
	}

	if !opts.SkipValidation {
		now := time.Now()
		count, size, err := cas.validate(ctx)
		dur := time.Since(now)
		slog.Info("validated blobs in CAS",
			"count", count,
			"size", fmt.Sprintf("%d MiB", size/1024/1024),
			"duration", dur,
			"hash_speed", fmt.Sprintf("%.2f MiB/s", float64(size)/dur.Seconds()/1024/1024))
		if err != nil {
			return nil, err
		}
	} else {
		slog.Warn("skipping CAS validation on startup")
	}

	return cas, nil
}

// validate checks that all files in the CAS are valid and returns the number of
// found blobs and their total size. Each configured digest function's blobs
// live under their own <function>/ root; other roots are ignored.
func (c *ContentAddressableStorage) validate(ctx context.Context) (count int, size int64, err error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.GOMAXPROCS(0))

	for _, fn := range c.fns {
		n, s, verr := c.validateRoot(ctx, g, c.roots[fn], fn)
		count += n
		size += s
		if verr != nil {
			return count, size, verr
		}
	}

	// Wait for all goroutines to complete
	if err = g.Wait(); err != nil {
		return count, size, err
	}
	return count, size, nil
}

// validateRoot walks one digest function's root directory, verifying file names
// and scheduling digest re-checks on g.
func (c *ContentAddressableStorage) validateRoot(ctx context.Context, g *errgroup.Group, root string, fn digest.Function) (count int, size int64, err error) {
	hexLen := fn.HexLen()

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Verify that there are no unexpected directories.
		if d.IsDir() {
			relPath, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if relPath == "." {
				return nil
			}
			if c.sharded && LooksLikeShardDir(relPath) {
				return nil
			}
			return fmt.Errorf("unexpected subdirectory %s", path)
		}

		// Exit early if context is cancelled.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Skip files whose name is not a valid digest of the expected length;
		// delete leftover temporary files from a crashed write.
		if stray, err := SkipStrayFile(path, d.Name(), hexLen); err != nil || stray {
			return err
		}

		// Keep stats about the found blobs.
		fi, err := d.Info()
		if err != nil {
			return err
		}
		count++
		size += fi.Size()

		// Validate file digests in parallel.
		g.Go(func() error {
			// Read the file and verify its digest.
			actualDigest, err := fn.FromFile(path)
			if err != nil {
				return fmt.Errorf("failed to read file %s: %w", path, err)
			}
			if actualDigest.Hash != d.Name() {
				slog.Error("file hash mismatch", "path", path, "expected", d.Name(), "actual", actualDigest.Hash)
				return fmt.Errorf("file %s has incorrect hash: expected %s, got %s", path, d.Name(), actualDigest.Hash)
			}
			return nil
		})

		return nil
	})
	return count, size, err
}

// Path returns the path to the file with digest d in the CAS. Each digest
// function's blobs live under their own <function>/ root. The configured
// functions' roots are precomputed; a non-configured function (only reachable
// through internal calls, since the RPC layer rejects non-advertised
// functions) falls back to joining the path on the fly.
func (c *ContentAddressableStorage) Path(fn digest.Function, d digest.Digest) string {
	dir, ok := c.roots[fn]
	if !ok {
		dir = filepath.Join(c.dataDir, fn.String())
	}
	if c.sharded {
		return filepath.Join(dir, d.Hash[:2], d.Hash)
	}
	return filepath.Join(dir, d.Hash)
}

// DigestKey returns the in-memory key ("<function>/<hash>") for a digest,
// namespacing same-length hashes from different digest functions. It is used
// for singleflight and dedup map keys.
func DigestKey(fn digest.Function, d digest.Digest) string {
	return fn.String() + "/" + d.Hash
}

// Stat returns os.FileInfo for the requested digest if it exists.
func (c *ContentAddressableStorage) Stat(fn digest.Function, d digest.Digest) (os.FileInfo, error) {
	p := c.Path(fn, d)

	fi, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &MissingBlobsError{Fn: fn, Blobs: []digest.Digest{d}}
		}
		return nil, err
	}

	if fi.Size() != d.SizeBytes {
		slog.Error("actual file size does not match digest", "size", fi.Size(), "digest", d)
		return nil, &MissingBlobsError{Fn: fn, Blobs: []digest.Digest{d}}
	}

	return fi, nil
}

// Has returns true if the requested digest exists in the CAS.
func (c *ContentAddressableStorage) Has(fn digest.Function, d digest.Digest) bool {
	if _, err := c.Stat(fn, d); err != nil {
		if _, ok := errors.AsType[*MissingBlobsError](err); !ok {
			// That's unexpected, let's log it.
			slog.Error("stat failed", "digest", d, "error", err)
		}
		return false
	}
	return true
}

// Open returns an io.ReadCloser for the requested digest if it exists.
// The returned ReadCloser is limited to the given offset and limit.
// The offset must be non-negative and no larger than the file size.
// A limit of 0 means no limit, and a limit that's larger than the file size is truncated to the file size.
func (c *ContentAddressableStorage) Open(fn digest.Function, d digest.Digest, offset int64, limit int64) (io.ReadCloser, error) {
	p := c.Path(fn, d)

	// TODO: check splice/<digest> for blob created by SpliceBlob
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &MissingBlobsError{Fn: fn, Blobs: []digest.Digest{d}}
		}
		return nil, err
	}

	// Ensure that the offset and limit are not negative and not larger than the file size.
	if offset < 0 || offset > d.SizeBytes || limit < 0 || limit > d.SizeBytes-offset {
		_ = f.Close()
		return nil, fs.ErrInvalid
	}

	// Seek to the requested offset if necessary.
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, err
		}
	}

	// Limit the returned reader to the requested size if necessary.
	if limit > 0 {
		return LimitReadCloser(f, limit), nil
	}

	return f, nil
}

// Get reads a file for the given digest from disk and returns its contents.
func (c *ContentAddressableStorage) Get(fn digest.Function, d digest.Digest) ([]byte, error) {
	// Just call Open and read the whole file.
	f, err := c.Open(fn, d, 0, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Error is safe to ignore, because we're just reading.
		_ = f.Close()
	}()
	return io.ReadAll(f)
}

// Put stores the given data in the CAS using digest function fn and returns its
// digest.
func (c *ContentAddressableStorage) Put(fn digest.Function, data []byte) (digest.Digest, error) {
	d := fn.FromBytes(data)
	_, err, _ := c.putSyncer.Do(DigestKey(fn, d), func() (any, error) {
		// If the file is already in the CAS, we're done.
		if c.Has(fn, d) {
			return nil, nil
		}

		// Add the file to the CAS.
		if err := atomicio.WriteFile(c.Path(fn, d), data); err != nil {
			return nil, err
		}
		return nil, nil
	})
	return d, err
}

// Adopt moves a file from the given path into the CAS.
// The digest is assumed to have been validated by the caller.
func (c *ContentAddressableStorage) Adopt(fn digest.Function, d digest.Digest, srcPath string) error {
	_, err, _ := c.putSyncer.Do(DigestKey(fn, d), func() (any, error) {
		// If the file is already in the CAS, we're done.
		if c.Has(fn, d) {
			if err := os.Remove(srcPath); err != nil {
				return nil, err
			}
			return nil, nil
		}

		// Move the file into the CAS.
		if err := os.Rename(srcPath, c.Path(fn, d)); err != nil {
			return nil, err
		}
		return nil, nil
	})
	return err
}

// LinkTo creates a link `path` pointing to the file with digest `d` in the CAS.
// If the operating system supports cloning files via copy-on-write semantics,
// the file is cloned instead of hard linked.
func (c *ContentAddressableStorage) LinkTo(fn digest.Function, d digest.Digest, path string) error {
	if err := FastCopy(c.Path(fn, d), path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &MissingBlobsError{Fn: fn, Blobs: []digest.Digest{d}}
		}
		return err
	}
	return nil
}

// Delete removes a file with digest d from the CAS.
func (c *ContentAddressableStorage) Delete(fn digest.Function, d digest.Digest) error {
	p := c.Path(fn, d)
	err := os.Remove(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_ = os.Remove(c.splitPath(fn, d))
	return nil
}
