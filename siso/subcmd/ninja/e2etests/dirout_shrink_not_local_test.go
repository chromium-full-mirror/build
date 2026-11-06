// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/ninjabuild"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/reapi/reapitest"
)

// TestBuild_DirOutputShrinkPrunedWhenNotLocal covers the shrink case for a build
// that leaves materialization to a consumer (output_local false for the dir).
// Build 1 materializes gendir/{a,b}; build 2 shrinks it to gendir/{a} with
// output_local off. Nothing flushes the tree in build 2, so a prune gated on
// NeedFlush would skip it and leave gendir/b on disk. That is not merely a
// leftover file: the on-demand flush walks the tree with ReadDir
// (expandFlushDirs), which picks up still-on-disk files into hashfs, so a
// later consumer would see gendir/b as a member of the directory output.
func TestBuild_DirOutputShrinkPrunedWhenNotLocal(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx := t.Context()
	dir := tempDir(t)
	setupFiles(t, dir, "TestBuild_DirOutputRemoteShrinkPruned", nil)

	dirResult := func(re *reapitest.Fake, members ...string) (*rpb.ActionResult, error) {
		var files []*rpb.FileNode
		for _, m := range members {
			content := []byte("content-" + m)
			if _, err := re.Put(ctx, content); err != nil {
				return nil, err
			}
			files = append(files, &rpb.FileNode{
				Name:   m,
				Digest: blob.FromBytes(digest.SHA256, "gendir/"+m, content).Digest().Proto(),
			})
		}
		tree := &rpb.Tree{Root: &rpb.Directory{Files: files}}
		treeBytes, err := proto.Marshal(tree)
		if err != nil {
			return nil, err
		}
		if _, err := re.Put(ctx, treeBytes); err != nil {
			return nil, err
		}
		treeDigest := blob.FromBytes(digest.SHA256, "gendir-tree", treeBytes)
		return &rpb.ActionResult{
			OutputDirectories: []*rpb.OutputDirectory{
				{Path: "gendir", TreeDigest: treeDigest.Digest().Proto()},
			},
		}, nil
	}

	execCount := 0
	fakere := &reapitest.Fake{
		ExecuteFunc: func(re *reapitest.Fake, action *rpb.Action) (*rpb.ActionResult, error) {
			execCount++
			members := []string{"a", "b"}
			if execCount >= 2 {
				members = []string{"a"} // build 2: gendir/ shrinks (b dropped)
			}
			res, err := dirResult(re, members...)
			if err != nil {
				return &rpb.ActionResult{ExitCode: 1, StderrRaw: fmt.Appendf(nil, "dirResult: %v", err)}, nil
			}
			return res, nil
		},
	}

	outputLocal := true
	runNinja := func(t *testing.T) error {
		t.Helper()
		var ds build.DataSource
		defer func() {
			if err := ds.Close(ctx); err != nil {
				t.Error(err)
			}
		}()
		ds.Client = reapitest.New(ctx, t, fakere)
		ds.Cache = ds.Client.CacheStore()
		opt, graph, cleanup := setupBuild(ctx, t, dir, hashfs.Option{
			StateFile:   ".siso_fs_state",
			DataSource:  ds,
			OutputLocal: func(context.Context, string) bool { return outputLocal },
		})
		defer cleanup()
		opt.REAPIClient = ds.Client
		_, err := ninjabuild.Run(ctx, graph, opt, nil, ninjabuild.RunNinjaOpts{})
		return err
	}

	// Build 1 materializes gendir/{a,b}.
	if err := runNinja(t); err != nil {
		t.Fatalf("build 1: %v", err)
	}
	for _, m := range []string{"a", "b"} {
		if _, err := os.Stat(filepath.Join(dir, "out/siso/gendir", m)); err != nil {
			t.Fatalf("after build 1, gendir/%s: %v; want present", m, err)
		}
	}

	// Change the input so the step re-executes (cache miss).
	if err := os.WriteFile(filepath.Join(dir, "input"), []byte("v2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Build 2 shrinks gendir/ to {a} with materialization left to consumers.
	outputLocal = false
	if err := runNinja(t); err != nil {
		t.Fatalf("build 2: %v", err)
	}
	if execCount < 2 {
		t.Fatalf("step did not re-execute in build 2 (execCount=%d); the shrink path was not exercised", execCount)
	}
	if _, err := os.Stat(filepath.Join(dir, "out/siso/gendir/b")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("gendir/b still on disk after the dir output shrank (err=%v); the prune was skipped because the build did not flush the tree", err)
	}
}
