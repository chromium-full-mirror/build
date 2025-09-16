// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"fmt"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/protobuf/proto"
)

// proto reads a proto message with the given digest from the CAS and unmarshals it into m.
func (c *ContentAddressableStorage) proto(d digest.Digest, m proto.Message) error {
	msgBytes, err := c.Get(d)
	if err != nil {
		return fmt.Errorf("failed to get protobuf from CAS: %w", err)
	}
	if err := proto.Unmarshal(msgBytes, m); err != nil {
		return fmt.Errorf("failed to unmarshal protobuf: %w", err)
	}
	return nil
}

// Action reads an Action message with the given digest from the CAS.
func (c *ContentAddressableStorage) Action(actionDigest digest.Digest) (*repb.Action, error) {
	action := &repb.Action{}
	if err := c.proto(actionDigest, action); err != nil {
		return nil, fmt.Errorf("failed to get Action message from CAS: %w", err)
	}
	return action, nil
}

// Command reads a Command message with the given digest from the CAS.
func (c *ContentAddressableStorage) Command(cmdDigest digest.Digest) (*repb.Command, error) {
	cmd := &repb.Command{}
	if err := c.proto(cmdDigest, cmd); err != nil {
		return nil, fmt.Errorf("failed to get Command message from CAS: %w", err)
	}
	return cmd, nil
}

// Directory reads a Directory message with the given digest from the CAS.
func (c *ContentAddressableStorage) Directory(dirDigest digest.Digest) (*repb.Directory, error) {
	dir := &repb.Directory{}
	if err := c.proto(dirDigest, dir); err != nil {
		return nil, fmt.Errorf("failed to get Directory message from CAS: %w", err)
	}
	return dir, nil
}

// FlattenDirectory reads a Directory message with the given digest from the CAS and returns a flat
// list of it and all subdirectories.
func (c *ContentAddressableStorage) FlattenDirectory(rootDigest digest.Digest) (dirs []*repb.Directory, err error) {
	// Create a queue of directories to process and add the root directory.
	dirQueue := []digest.Digest{rootDigest}

	// Iteratively process the directories.
	for len(dirQueue) > 0 {
		// Take a directoryNode from the queue.
		currentDigest := dirQueue[0]
		dirQueue = dirQueue[1:]

		// Get the blob for the directory message from the CAS.
		directory, err := c.Directory(currentDigest)
		if err != nil {
			return nil, err
		}

		// Add the directory to the response.
		dirs = append(dirs, directory)

		// Add all subdirectory nodes to the queue.
		for _, subDirNode := range directory.Directories {
			// Parse the digest.
			subDigest, err := digest.NewFromProto(subDirNode.Digest)
			if err != nil {
				return nil, fmt.Errorf("invalid digest: %v", err)
			}
			dirQueue = append(dirQueue, subDigest)
		}
	}

	return dirs, nil
}
