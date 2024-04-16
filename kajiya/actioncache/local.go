// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package actioncache

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/proto"

	"infra/build/kajiya/atomicio"
)

// ActionCache is a simple action cache implementation that stores ActionResults on the local disk.
type ActionCache struct {
	dataDir string             // directory where the action results are stored
	syncer  singleflight.Group // synchronization mechanism to prevent concurrent puts of the same action
}

// New creates a new local ActionCache. The data directory is created if it does not exist.
func New(dataDir string) (*ActionCache, error) {
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

	return &ActionCache{
		dataDir: dataDir,
	}, nil
}

// path returns the path to the file with digest d in the action cache.
func (c *ActionCache) path(d digest.Digest) string {
	return filepath.Join(c.dataDir, d.Hash[:2], d.Hash)
}

// Get returns the cached ActionResult for the given digest.
func (c *ActionCache) Get(actionDigest digest.Digest) (*repb.ActionResult, error) {
	p := c.path(actionDigest)

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
func (c *ActionCache) Put(actionDigest digest.Digest, ar *repb.ActionResult) error {
	_, err, _ := c.syncer.Do(actionDigest.Hash, func() (interface{}, error) {
		// Marshal the action result. We use deterministic marshalling to ensure
		// that the below comparison works correctly.
		actionResultRaw, err := proto.MarshalOptions{Deterministic: true}.Marshal(ar)
		if err != nil {
			return nil, err
		}

		// Check if the action result is already in the cache. If yes
		// and it is the same as the one we want to store, we can skip
		// writing it to disk.
		buf, err := os.ReadFile(c.path(actionDigest))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if err == nil && bytes.Equal(buf, actionResultRaw) {
			// Already cached and the same result, nothing to do.
			return nil, nil
		}

		// Store the action result in our action cache.
		err = atomicio.WriteFile(c.path(actionDigest), actionResultRaw)
		return nil, err
	})
	return err
}

// Remove deletes the cached ActionResult for the given digest.
func (c *ActionCache) Remove(d digest.Digest) error {
	return fmt.Errorf("not implemented yet")
}
