// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"errors"
	"path/filepath"
	"testing"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/ninjabuild"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/reapi/reapitest"
	"go.chromium.org/build/siso/scandeps"
)

func TestBuild_ScanDeps_ClangCL_FI(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx := t.Context()
	dir := tempDir(t)

	runNinjaTest := func(t *testing.T, fakere *reapitest.Fake) (build.Stats, error) {
		t.Helper()
		var ds build.DataSource
		defer func() {
			err := ds.Close(ctx)
			if err != nil {
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
		return ninjabuild.Run(ctx, graph, opt, nil, ninjabuild.RunNinjaOpts{})
	}

	setupFiles(t, dir, t.Name(), nil)
	fakere := &reapitest.Fake{
		ExecuteFunc: func(fakere *reapitest.Fake, action *rpb.Action) (*rpb.ActionResult, error) {
			t.Logf("-- remote action %s", action)
			tree := reapitest.InputTree{CAS: fakere.CAS, Root: action.InputRootDigest}
			_, err := tree.LookupFileNode(ctx, "third_party/ffmpeg/compat/msvcrt/snprintf.h")
			if err != nil {
				t.Logf("-- error: third_party/ffmpeg/compat/msvc/snprintf.h does not exists")
				return &rpb.ActionResult{
					ExitCode:  1,
					StderrRaw: []byte("<built-in>(1,10): fatal error: 'compat/msvcrt/snprintf.h' file not found\n"),
				}, nil
			}
			t.Logf("-- third_party/ffmpeg/compat/msvc/snprintf.h exists")
			return &rpb.ActionResult{
				ExitCode: 0,
				OutputFiles: []*rpb.OutputFile{
					{
						Path:   "obj/third_party/ffmpeg/m.obj",
						Digest: digest.SHA256.Empty().Proto(),
					},
				},
			}, nil
		},
	}

	stats, err := runNinjaTest(t, fakere)
	if err != nil {
		t.Fatalf("ninja err: %v; want nil err", err)
	}
	if stats.Done != stats.Total || stats.Remote != 1 || stats.Local != 0 {
		t.Errorf("stats done=%d total=%d remote=%d local=%d; want remote=1 local=0", stats.Done, stats.Total, stats.Remote, stats.Local)
	}
}

func TestBuild_ScanDeps_Timeout(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx := t.Context()
	dir := tempDir(t)

	runNinjaTest := func(t *testing.T, fakere *reapitest.Fake) (build.Stats, error) {
		t.Helper()
		var ds build.DataSource
		defer func() {
			err := ds.Close(ctx)
			if err != nil {
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
		return ninjabuild.Run(ctx, graph, opt, nil, ninjabuild.RunNinjaOpts{})
	}
	scandeps.SetErrForTest(errors.New("scandeps err"))
	defer scandeps.SetErrForTest(nil)

	setupFiles(t, dir, "TestBuild_ScanDeps_ClangCL_FI", nil)

	fakere := &reapitest.Fake{
		ExecuteFunc: func(fakere *reapitest.Fake, action *rpb.Action) (*rpb.ActionResult, error) {
			t.Logf("-- remote action %s", action)
			tree := reapitest.InputTree{CAS: fakere.CAS, Root: action.InputRootDigest}
			_, err := tree.LookupFileNode(ctx, "third_party/ffmpeg/compat/msvcrt/snprintf.h")
			if err != nil {
				t.Logf("-- error: third_party/ffmpeg/compat/msvc/snprintf.h does not exists")
				return &rpb.ActionResult{
					ExitCode:  1,
					StderrRaw: []byte("<built-in>(1,10): fatal error: 'compat/msvcrt/snprintf.h' file not found\n"),
				}, nil
			}
			t.Logf("-- third_party/ffmpeg/compat/msvc/snprintf.h exists")
			return &rpb.ActionResult{
				ExitCode: 0,
				OutputFiles: []*rpb.OutputFile{
					{
						Path:   "obj/third_party/ffmpeg/m.obj",
						Digest: digest.SHA256.Empty().Proto(),
					},
				},
			}, nil
		},
	}

	stats, err := runNinjaTest(t, fakere)
	if err != nil {
		t.Fatalf("ninja err: %v; want nil err", err)
	}
	if stats.Done != stats.Total || stats.Remote != 0 || stats.Local != 1 || stats.ScanDepsFailed != 1 {
		t.Errorf("stats done=%d total=%d remote=%d local=%d scanDepsFailed=%d; want remote=0 local=1 scanDepsFailed=1", stats.Done, stats.Total, stats.Remote, stats.Local, stats.ScanDepsFailed)
	}
}

func TestBuild_ScanDeps_StepInputs(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx := t.Context()
	dir := tempDir(t)

	runNinjaTest := func(t *testing.T, fakere *reapitest.Fake) (build.Stats, error) {
		t.Helper()
		var ds build.DataSource
		defer func() {
			err := ds.Close(ctx)
			if err != nil {
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
		return ninjabuild.Run(ctx, graph, opt, nil, ninjabuild.RunNinjaOpts{})
	}
	setupFiles(t, dir, t.Name(), nil)
	fakere := &reapitest.Fake{
		ExecuteFunc: func(fakere *reapitest.Fake, action *rpb.Action) (*rpb.ActionResult, error) {
			t.Logf("-- remote action %s", action)
			tree := reapitest.InputTree{CAS: fakere.CAS, Root: action.InputRootDigest}
			_, err := tree.LookupFileNode(ctx, "build/config/plugin_input.txt")
			if err != nil {
				t.Logf("-- error: build/config/plugin_input.txt does not exist")
				return &rpb.ActionResult{
					ExitCode:  1,
					StderrRaw: []byte("<built-in>(1,10): fatal error: 'build/config/plugin_input.txt' file not found\n"),
				}, nil
			}
			_, err = tree.LookupFileNode(ctx, "build/config/header_is_ready")
			if err == nil {
				t.Logf("-- error: build/config/header_is_ready exists")
				return &rpb.ActionResult{
					ExitCode:  1,
					StderrRaw: []byte("unnecessary file build/config/header_is_ready exists in input\n"),
				}, nil
			}
			depfile, err := fakere.Put(ctx, []byte("obj/base/base.o: ../../base/base.cc ../../base/base.h"))
			if err != nil {
				return &rpb.ActionResult{
					ExitCode:  1,
					StderrRaw: []byte("failed to write obj/base/base.o.d"),
				}, nil
			}
			return &rpb.ActionResult{
				ExitCode: 0,
				OutputFiles: []*rpb.OutputFile{
					{
						Path:   "obj/base/base.o",
						Digest: digest.SHA256.Empty().Proto(),
					},
					{
						Path:   "obj/base/base.o.d",
						Digest: depfile,
					},
				},
			}, nil
		},
	}

	stats, err := runNinjaTest(t, fakere)
	if err != nil {
		t.Fatalf("ninja err: %v; want nil err", err)
	}
	if stats.Done != stats.Total || stats.Remote != 1 || stats.Local != 0 {
		t.Errorf("stats done=%d total=%d remote=%d local=%d; want remote=1 local=0", stats.Done, stats.Total, stats.Remote, stats.Local)
	}
}

// Test that unreferenced sibling files in a directory scanned by scandeps
// are not recorded in state entries or hashed on subsequent builds. b/360961799
func TestBuild_ScanDeps_UnrelatedSibling(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx := t.Context()
	dir := tempDir(t)

	var ds build.DataSource
	defer func() {
		err := ds.Close(ctx)
		if err != nil {
			t.Error(err)
		}
	}()
	fakere := &reapitest.Fake{
		ExecuteFunc: func(fakere *reapitest.Fake, action *rpb.Action) (*rpb.ActionResult, error) {
			depfile, err := fakere.Put(ctx, []byte("obj/base/base.o: ../../base/base.cc ../../base/base.h"))
			if err != nil {
				return &rpb.ActionResult{
					ExitCode:  1,
					StderrRaw: []byte("failed to write obj/base/base.o.d"),
				}, nil
			}
			return &rpb.ActionResult{
				ExitCode: 0,
				OutputFiles: []*rpb.OutputFile{
					{
						Path:   "obj/base/base.o",
						Digest: digest.SHA256.Empty().Proto(),
					},
					{
						Path:   "obj/base/base.o.d",
						Digest: depfile,
					},
				},
			}, nil
		},
	}
	ds.Client = reapitest.New(ctx, t, fakere)
	ds.Cache = ds.Client.CacheStore()

	runNinjaTest := func(t *testing.T) (build.Stats, error) {
		t.Helper()
		opt, graph, cleanup := setupBuild(ctx, t, dir, hashfs.Option{
			StateFile:  ".siso_fs_state",
			DataSource: ds,
		})
		defer cleanup()
		opt.REAPIClient = ds.Client
		return ninjabuild.Run(ctx, graph, opt, nil, ninjabuild.RunNinjaOpts{})
	}

	setupFiles(t, dir, t.Name(), nil)
	unrelated := filepath.ToSlash(filepath.Join(dir, "base/unrelated.data"))

	t.Logf("-- first build")
	stats, err := runNinjaTest(t)
	if err != nil {
		t.Fatalf("ninja err: %v; want nil err", err)
	}
	if stats.Done != stats.Total || stats.Remote != 1 || stats.Local != 0 {
		t.Errorf("stats done=%d total=%d remote=%d local=%d; want remote=1 local=0", stats.Done, stats.Total, stats.Remote, stats.Local)
	}

	st, err := hashfs.Load(ctx, hashfs.Option{StateFile: filepath.Join(dir, "out/siso/.siso_fs_state")})
	if err != nil {
		t.Fatalf("hashfs.Load err: %v", err)
	}
	for _, entry := range st.Entries {
		if entry.Name == unrelated {
			t.Errorf("state entries contains unreferenced sibling %q (digest=%v)", unrelated, entry.Digest)
		}
	}

	t.Logf("-- second build (no-op)")
	stats, err = runNinjaTest(t)
	if err != nil {
		t.Fatalf("ninja err: %v; want nil err", err)
	}
	if stats.Done != stats.Total || stats.Remote != 0 || stats.Local != 0 {
		t.Errorf("stats done=%d total=%d remote=%d local=%d; want remote=0 local=0", stats.Done, stats.Total, stats.Remote, stats.Local)
	}

	st, err = hashfs.Load(ctx, hashfs.Option{StateFile: filepath.Join(dir, "out/siso/.siso_fs_state")})
	if err != nil {
		t.Fatalf("hashfs.Load err: %v", err)
	}
	for _, entry := range st.Entries {
		if entry.Name == unrelated {
			t.Errorf("state entries contains unreferenced sibling %q (digest=%v)", unrelated, entry.Digest)
		}
	}
}
