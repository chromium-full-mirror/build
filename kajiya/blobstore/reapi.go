// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package blobstore

import (
	"fmt"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	"google.golang.org/protobuf/proto"
)

// Proto reads a proto message with the given digest from the CAS and unmarshals it into m.
func (c *ContentAddressableStorage) Proto(d digest.Digest, m proto.Message) error {
	msgBytes, err := c.Get(d)
	if err != nil {
		return fmt.Errorf("failed to get protobuf from CAS: %w", err)
	}
	if err := proto.Unmarshal(msgBytes, m); err != nil {
		return fmt.Errorf("failed to unmarshal protobuf: %w", err)
	}
	return nil
}
