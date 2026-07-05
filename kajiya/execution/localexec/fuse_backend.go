// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build linux

package localexec

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"

	"github.com/hanwen/go-fuse/v2/fuse"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/model"
)

// fuseBackend bundles the per-process FUSE machinery required by the
// FuseFS sandbox strategy: the mounted FUSE server, the CASRoot that
// serves dynamic per-sandbox subtrees, and the mountpoint path that
// per-sandbox overlays use as their lower-directory base.
//
// It exists so that the FUSE-only lifecycle (mount on Executor.New,
// unmount on Executor.Close, register/unregister per action) lives in one
// place instead of being smeared across three executor fields and a pair
// of conditional code paths in Sandbox.
type fuseBackend struct {
	root       *CASRoot
	server     *fuse.Server
	mountpoint string
}

// newFuseBackend prepares the mountpoint directory, sweeps any stale
// FUSE mount left behind by a previous crashed run, and mounts a new
// CAS-backed FUSE filesystem at mountpoint.
func newFuseBackend(mountpoint string) (*fuseBackend, error) {
	if err := os.Mkdir(mountpoint, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("failed to create FUSE mountpoint: %w", err)
	}
	// Try to unmount a stale FUSE mount from a previous run that may have
	// crashed without cleaning up.
	_ = exec.Command("fusermount3", "-u", mountpoint).Run()

	root, server, err := MountCASFS(mountpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to mount CAS FUSE filesystem: %w", err)
	}
	slog.Info("CAS FUSE filesystem mounted", "mountpoint", mountpoint)
	return &fuseBackend{
		root:       root,
		server:     server,
		mountpoint: mountpoint,
	}, nil
}

// cleanFuseMountpoint best-effort unmounts any stale FUSE mount at
// mountpoint and removes the empty directory. Used by Executor.New when
// the FuseFS strategy is *not* selected, to clean up after a previous
// run that did use FuseFS. Errors are logged at warn level since they
// don't block a non-FUSE startup.
func cleanFuseMountpoint(mountpoint string) {
	_ = exec.Command("fusermount3", "-u", mountpoint).Run()
	if err := os.Remove(mountpoint); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("failed to remove FUSE mountpoint", "error", err)
	}
}

// Close unmounts the FUSE filesystem and releases CASRoot resources.
// Safe to call on a nil receiver, which is how Executor.Close handles
// non-FUSE strategies.
func (b *fuseBackend) Close() error {
	if b == nil {
		return nil
	}
	b.root.Close()
	if err := b.server.Unmount(); err != nil {
		return fmt.Errorf("failed to unmount CAS FUSE filesystem: %w", err)
	}
	slog.Info("CAS FUSE filesystem unmounted")
	return nil
}

// RegisterSandbox creates a virtual subtree for the input trie of one
// action and returns the on-disk path inside the FUSE mount that should
// be used as the overlayfs lower directory.
func (b *fuseBackend) RegisterSandbox(sandboxID string, trie *model.DirectoryTrie, fn digest.Function, cas *blobstore.ContentAddressableStorage, recorder *AccessRecorder) (string, error) {
	return b.root.RegisterSandbox(sandboxID, trie, fn, cas, b.mountpoint, recorder)
}

// UnregisterSandbox removes the sandbox's virtual subtree, dropping its
// reference from the FUSE root inode. Shared file/directory inodes
// remain alive for reuse by other sandboxes.
func (b *fuseBackend) UnregisterSandbox(sandboxID string) {
	b.root.UnregisterSandbox(sandboxID)
}
