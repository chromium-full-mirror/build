// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"fmt"
	"strings"
	"testing"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/ninjabuild"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/reapi/reapitest"
)

// A remote input inside a precomputed subtree, such as the compiler in the
// toolchain dir, replaces the local file there.
func TestBuild_RemoteInputs_PrecomputedSubtree(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx := t.Context()
	dir := tempDir(t)
	setupFiles(t, dir, t.Name(), nil)

	wantCompiler := blob.FromBytes(digest.SHA256, "clang++", []byte("remote clang\n")).Digest()
	fakere := &reapitest.Fake{
		ExecuteFunc: func(fakere *reapitest.Fake, action *rpb.Action) (*rpb.ActionResult, error) {
			tree := reapitest.InputTree{CAS: fakere.CAS, Root: action.InputRootDigest}
			fail := func(msg string) (*rpb.ActionResult, error) {
				var buf strings.Builder
				if err := tree.Dump(ctx, &buf); err != nil {
					t.Errorf("failed to dump: %v", err)
				}
				t.Logf("%s\ninput tree:\n%s", msg, buf.String())
				return &rpb.ActionResult{ExitCode: 1, StderrRaw: []byte(msg)}, nil
			}
			fn, err := tree.LookupFileNode(ctx, "toolchain/bin/clang++")
			if err != nil {
				return fail(fmt.Sprintf("no compiler: %v", err))
			}
			if got := digest.FromProto(fn.Digest); got != wantCompiler {
				return fail(fmt.Sprintf("compiler digest %s; want %s", got, wantCompiler))
			}
			if _, err := tree.LookupFileNode(ctx, "toolchain/lib/clang/include/stddef.h"); err != nil {
				return fail(fmt.Sprintf("subtree lost a header: %v", err))
			}
			if _, err := tree.LookupFileNode(ctx, "toolchain/bin/tool"); err == nil {
				return fail("subtree got toolchain/bin/tool, which the step doesn't have")
			}
			out, err := fakere.Put(ctx, []byte("obj"))
			if err != nil {
				return fail(fmt.Sprintf("failed to put output: %v", err))
			}
			dep, err := fakere.Put(ctx, []byte("obj/foo.o: ../../src/foo.cc\n"))
			if err != nil {
				return fail(fmt.Sprintf("failed to put depfile: %v", err))
			}
			return &rpb.ActionResult{
				OutputFiles: []*rpb.OutputFile{
					{Path: "obj/foo.o", Digest: out},
					{Path: "obj/foo.o.d", Digest: dep},
				},
			}, nil
		},
	}

	var ds build.DataSource
	defer func() {
		if err := ds.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	ds.Client = reapitest.New(ctx, t, fakere)
	ds.Cache = ds.Client.CacheStore()
	opt, graph, cleanup := setupBuild(ctx, t, dir, hashfs.Option{
		StateFile:  ".siso_fs_state",
		DataSource: ds,
	})
	defer cleanup()
	opt.REAPIClient = ds.Client
	stats, err := ninjabuild.Run(ctx, graph, opt, nil, ninjabuild.RunNinjaOpts{})
	if err != nil {
		t.Fatalf("ninja: %v; want nil err", err)
	}
	if stats.Done != stats.Total || stats.Remote != 1 {
		t.Errorf("done=%d remote=%d total=%d; want done=total, remote=1", stats.Done, stats.Remote, stats.Total)
	}
}
