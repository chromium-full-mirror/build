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

// Proto reads a proto message with the given digest from the CAS and unmarshals it into m.
func (c *ContentAddressableStorage) Proto(d *repb.Digest, m proto.Message) error {
	msgDigest, err := digest.NewFromProto(d)
	if err != nil {
		return fmt.Errorf("failed to parse digest: %w", err)
	}
	msgBytes, err := c.Get(msgDigest)
	if err != nil {
		return fmt.Errorf("failed to get protobuf from CAS: %w", err)
	}
	if err := proto.Unmarshal(msgBytes, m); err != nil {
		return fmt.Errorf("failed to unmarshal protobuf: %w", err)
	}
	return nil
}
