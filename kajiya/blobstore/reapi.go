// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// proto reads a proto message with the given digest from the CAS and unmarshals it into m.
func (c *ContentAddressableStorage) proto(fn digest.Function, d digest.Digest, m proto.Message) error {
	msgBytes, err := c.Get(fn, d)
	if err != nil {
		return fmt.Errorf("failed to get protobuf from CAS: %w", err)
	}
	if err := proto.Unmarshal(msgBytes, m); err != nil {
		return fmt.Errorf("failed to unmarshal protobuf: %w", err)
	}
	return nil
}

// Action reads an Action message with the given digest from the CAS.
func (c *ContentAddressableStorage) Action(fn digest.Function, actionDigest digest.Digest) (*repb.Action, error) {
	action := &repb.Action{}
	if err := c.proto(fn, actionDigest, action); err != nil {
		return nil, fmt.Errorf("failed to get Action message from CAS: %w", err)
	}
	return action, nil
}

// Command reads a Command message with the given digest from the CAS.
func (c *ContentAddressableStorage) Command(fn digest.Function, cmdDigest digest.Digest) (*repb.Command, error) {
	cmd := &repb.Command{}
	if err := c.proto(fn, cmdDigest, cmd); err != nil {
		return nil, fmt.Errorf("failed to get Command message from CAS: %w", err)
	}
	return cmd, nil
}

// Directory reads a Directory message with the given digest from the CAS.
func (c *ContentAddressableStorage) Directory(fn digest.Function, dirDigest digest.Digest) (*repb.Directory, error) {
	dir := &repb.Directory{}
	if err := c.proto(fn, dirDigest, dir); err != nil {
		return nil, fmt.Errorf("failed to get Directory message from CAS: %w", err)
	}
	return dir, nil
}

// FlattenDirectory reads a Directory message with the given digest from the CAS and returns flat
// lists with the digests as well as the messages of the root directory and all subdirectories.
// Subdirectories use the same digest function as the root.
func (c *ContentAddressableStorage) FlattenDirectory(fn digest.Function, rootDigest digest.Digest) (dirDigests []digest.Digest, dirs []*repb.Directory, err error) {
	// Create a queue of directories to process and add the root directory.
	dirDigests = []digest.Digest{rootDigest}

	// Iteratively process the directories.
	for i := 0; i < len(dirDigests); i++ {
		// Get the blob for the directory message from the CAS.
		directory, err := c.Directory(fn, dirDigests[i])
		if err != nil {
			return nil, nil, err
		}

		// Add the directory to the response.
		dirs = append(dirs, directory)

		// Add all subdirectory nodes to the queue.
		for _, subDirNode := range directory.Directories {
			// Parse the digest.
			subDigest, err := fn.FromProto(subDirNode.Digest)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid digest: %v", err)
			}
			dirDigests = append(dirDigests, subDigest)
		}
	}

	return dirDigests, dirs, nil
}
