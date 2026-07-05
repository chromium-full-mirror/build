// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package actioncache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/atomicio"
	"go.chromium.org/build/kajiya/blobstore"
)

// ActionCache is a simple action cache implementation that stores ActionResults on the local disk.
type ActionCache struct {
	dataDir string                               // directory where the action results are stored
	sharded bool                                 // whether action results are placed under {00, 01, ..., ff} subdirs
	syncer  singleflight.Group                   // synchronization mechanism to prevent concurrent puts of the same action
	cas     *blobstore.ContentAddressableStorage // CAS for validating referenced blobs
}

// Options configures a local ActionCache.
type Options struct {
	// Sharded enables a two-level on-disk layout where action results are
	// placed under subdirectories named by the first byte of their hash
	// ({00, 01, ..., ff}). Recommended for production where the cache may
	// hold many results; the zero value (no sharding) avoids 256 mkdir
	// calls and is cheaper for short-lived caches (e.g. in tests).
	Sharded bool
}

// New creates a new local ActionCache with default options. The data directory is created if it does not exist.
func New(ctx context.Context, dataDir string, cas *blobstore.ContentAddressableStorage) (*ActionCache, error) {
	return NewWithOpts(ctx, dataDir, cas, Options{})
}

// NewWithOpts creates a new local ActionCache. The data directory is created if it does not exist.
func NewWithOpts(ctx context.Context, dataDir string, cas *blobstore.ContentAddressableStorage, opts Options) (*ActionCache, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data directory must be specified")
	}

	if err := os.Mkdir(dataDir, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}

	// Ensure that our data directory has the correct layout.
	if _, err := blobstore.EnsureLayout(dataDir, opts.Sharded); err != nil {
		return nil, fmt.Errorf("migrating action cache layout: %w", err)
	}

	ac := &ActionCache{
		dataDir: dataDir,
		sharded: opts.Sharded,
		cas:     cas,
	}

	now := time.Now()
	count, blobs, err := ac.validate(ctx)
	dur := time.Since(now)
	slog.Info("validated results in action cache",
		"count", count,
		"referenced_blobs", len(blobs),
		"duration", dur)

	return ac, err
}

// isValidSubdir returns true if the given subdirectory name is valid inside the data directory.
// The provided path must be relative to the data directory.
func (c *ActionCache) isValidSubdir(s string) bool {
	if s == "." {
		return true
	}
	return c.sharded && len(s) == 2 && digest.IsHex(s[0]) && digest.IsHex(s[1])
}

// validateCache checks that all actions in the cache are valid.
func (c *ActionCache) validate(ctx context.Context) (count int, blobs []digest.Digest, err error) {
	var blobsMu sync.Mutex

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.GOMAXPROCS(0))

	// Walk through all files in our data directory and verify that they have the
	// correct hash and size.
	err = filepath.WalkDir(c.dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Verify that there are no unexpected directories.
		if d.IsDir() {
			relPath, err := filepath.Rel(c.dataDir, path)
			if err != nil {
				return err
			}
			if c.isValidSubdir(relPath) {
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

		// Keep stats about the found action results.
		fi, err := d.Info()
		if err != nil {
			return err
		}
		count++

		// Validate file digests in parallel.
		g.Go(func() error {
			actionDigest, err := digest.SHA256.Validate(d.Name(), fi.Size())
			if err != nil {
				return err
			}
			b, err := c.validateAction(digest.SHA256, actionDigest)
			blobsMu.Lock()
			blobs = append(blobs, b...)
			blobsMu.Unlock()
			return err
		})

		return nil
	})
	if err != nil {
		return 0, nil, err
	}

	// Wait for all goroutines to complete
	if err = g.Wait(); err != nil {
		return 0, nil, err
	}

	// Dedupe the list of blobs.
	slices.SortFunc(blobs, func(a, b digest.Digest) int { return strings.Compare(a.Hash, b.Hash) })
	blobs = slices.Compact(blobs)

	return count, blobs, nil
}

// validateAction checks that the action with the given digest is present, valid
// and that all blobs referenced by it exist in the CAS.
func (c *ActionCache) validateAction(fn digest.Function, d digest.Digest) (blobs []digest.Digest, err error) {
	// Get the ActionResult from the cache.
	actionResult, err := c.Get(fn, d)
	if err != nil {
		return nil, err
	}

	// Referenced blobs use the same digest function as the action.
	empty := fn.Empty()

	// Helper function to avoid duplicating error handling code below.
	addBlob := func(h *repb.Digest) error {
		// Empty digests are guaranteed to be present in the CAS and just waste memory.
		if h == nil || (h.SizeBytes == 0 && h.Hash == empty.Hash) {
			return nil
		}

		// Parse the digest.
		d, err := fn.FromProto(h)
		if err != nil {
			return err
		}

		// Check that all referenced blobs exist in the CAS.
		if !c.cas.Has(fn, d) {
			return fmt.Errorf("action result from CAS missing referenced blob %s", d)
		}

		blobs = append(blobs, d)

		return nil
	}

	// Collect all referenced blobs.
	blobs = make([]digest.Digest, 0, len(actionResult.OutputFiles)+2)
	for _, file := range actionResult.OutputFiles {
		if err = addBlob(file.Digest); err != nil {
			return nil, err
		}
	}
	if err = addBlob(actionResult.StdoutDigest); err != nil {
		return nil, err
	}
	if err = addBlob(actionResult.StderrDigest); err != nil {
		return nil, err
	}
	for _, dir := range actionResult.OutputDirectories {
		if dir.TreeDigest != nil {
			return nil, fmt.Errorf("action result from CAS unexpectedly had an output dir with a tree digest")
		}
		if dir.RootDirectoryDigest == nil {
			return nil, fmt.Errorf("action result from CAS had an output dir with a missing root directory digest")
		}
		rootDigest, err := fn.FromProto(dir.RootDirectoryDigest)
		if err != nil {
			return nil, err
		}
		dirDigests, dirs, err := c.cas.FlattenDirectory(fn, rootDigest)
		if err != nil {
			return nil, err
		}
		blobs = append(blobs, dirDigests...)
		for _, dir := range dirs {
			for _, file := range dir.Files {
				if err = addBlob(file.Digest); err != nil {
					return nil, err
				}
			}
		}
	}

	return blobs, nil
}

// path returns the path to the file with digest d in the action cache.
func (c *ActionCache) path(_ digest.Function, d digest.Digest) string {
	if c.sharded {
		return filepath.Join(c.dataDir, d.Hash[:2], d.Hash)
	}
	return filepath.Join(c.dataDir, d.Hash)
}

// Get returns the cached ActionResult for the given digest.
func (c *ActionCache) Get(fn digest.Function, actionDigest digest.Digest) (*repb.ActionResult, error) {
	p := c.path(fn, actionDigest)

	// Read the action result for the requested action into a byte slice.
	buf, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}

	// Unmarshal it into an ActionResult message and return it to the client.
	actionResult := &repb.ActionResult{}
	if err := proto.Unmarshal(buf, actionResult); err != nil {
		return nil, err
	}

	return actionResult, nil
}

// Put stores the given ActionResult for the given digest.
func (c *ActionCache) Put(fn digest.Function, actionDigest digest.Digest, ar *repb.ActionResult) error {
	_, err, _ := c.syncer.Do(actionDigest.Hash, func() (any, error) {
		// Marshal the action result. We use deterministic marshalling to ensure
		// that the below comparison works correctly.
		actionResultRaw, err := proto.MarshalOptions{Deterministic: true}.Marshal(ar)
		if err != nil {
			return nil, err
		}

		// Check if the action result is already in the cache. If yes
		// and it is the same as the one we want to store, we can skip
		// writing it to disk.
		buf, err := os.ReadFile(c.path(fn, actionDigest))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if err == nil && bytes.Equal(buf, actionResultRaw) {
			// Already cached and the same result, nothing to do.
			return nil, nil
		}

		// Store the action result in our action cache.
		err = atomicio.WriteFile(c.path(fn, actionDigest), actionResultRaw)
		return nil, err
	})
	return err
}

// Remove deletes the cached ActionResult for the given digest.
func (c *ActionCache) Remove(fn digest.Function, d digest.Digest) error {
	return fmt.Errorf("not implemented yet")
}
