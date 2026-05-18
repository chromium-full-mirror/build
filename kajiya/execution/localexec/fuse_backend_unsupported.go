// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build !linux

package localexec

import (
	"errors"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/model"
)

type fuseBackend struct{}

func newFuseBackend(string) (*fuseBackend, error) {
	return nil, errors.New("FuseFS is only supported on Linux")
}

func cleanFuseMountpoint(string) {}

func (b *fuseBackend) Close() error {
	return nil
}

func (b *fuseBackend) RegisterSandbox(string, *model.DirectoryTrie, *blobstore.ContentAddressableStorage) (string, error) {
	return "", errors.New("FuseFS is only supported on Linux")
}

func (b *fuseBackend) UnregisterSandbox(string) {}
