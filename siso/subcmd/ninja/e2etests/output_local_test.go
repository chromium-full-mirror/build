// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/ninjabuild"
	"go.chromium.org/build/siso/hashfs"
	pb "go.chromium.org/build/siso/hashfs/proto"
	"go.chromium.org/build/siso/reapi/reapitest"
)

func TestBuild_OutputLocal(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx := t.Context()
	dir := tempDir(t)

	runNinjaTest := func(t *testing.T, outputLocal bool, fakere *reapitest.Fake) (build.Stats, error) {
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
			StateFile:   ".siso_fs_state",
			OutputLocal: func(context.Context, string) bool { return outputLocal },
			DataSource:  ds,
		})
		defer cleanup()
		opt.REAPIClient = ds.Client
		return ninjabuild.Run(ctx, graph, opt, nil, ninjabuild.RunNinjaOpts{})
	}
	setupFiles(t, dir, t.Name(), nil)

	fakere := &reapitest.Fake{
		ExecuteFunc: func(fakere *reapitest.Fake, action *rpb.Action) (*rpb.ActionResult, error) {
			tree := reapitest.InputTree{CAS: fakere.CAS, Root: action.InputRootDigest}
			fn, err := tree.LookupFileNode(ctx, "input")
			if err != nil {
				t.Logf("missing input: %v", err)
				return &rpb.ActionResult{
					ExitCode:  1,
					StderrRaw: fmt.Appendf(nil, "../../input: File not found: %v", err),
				}, nil
			}
			return &rpb.ActionResult{
				ExitCode: 0,
				OutputFiles: []*rpb.OutputFile{
					{
						Path:   "out0",
						Digest: fn.Digest,
					},
					{
						Path:   "out1",
						Digest: fn.Digest,
					},
				},
			}, nil
		},
	}

	checkFile := func(dir, name string, data []byte) error {
		buf, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if diff := cmp.Diff(data, buf); diff != "" {
			return fmt.Errorf("unexpected content for %q: -want +got:\n%s", name, diff)
		}
		return nil
	}

	t.Logf("-- first build. output_local=true")
	stats, err := runNinjaTest(t, true, fakere)
	if err != nil {
		t.Fatalf("ninja err: %v", err)
	}
	if stats.Done != stats.Total || stats.Total != 3 || stats.Remote != 1 || stats.Local != 2 {
		t.Errorf("done=%d total=%d remote=%d local=%d; want done=total=3 remote=1 local=2: %#v", stats.Done, stats.Total, stats.Remote, stats.Local, stats)
	}

	buf, err := os.ReadFile(filepath.Join(dir, "input"))
	if err != nil {
		t.Fatal(err)
	}
	data := buf
	for _, name := range []string{"out0", "out1", "out2", "out3"} {
		err = checkFile(dir, filepath.Join("out/siso", name), data)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	t.Logf("-- confirm no op")
	stats, err = runNinjaTest(t, true, fakere)
	if err != nil {
		t.Fatalf("ninja err: %v", err)
	}
	if stats.Done != stats.Total || stats.Total != 3 || stats.Skipped != 3 {
		t.Errorf("done=%d total=%d skipped=%d; want done=total=skipped=3; %#v", stats.Done, stats.Total, stats.Skipped, stats)
	}

	data = append(buf, []byte(" modified")...)
	modifyFile(t, dir, "input", func([]byte) []byte { return data })

	err = checkFile(dir, "input", data)
	if err != nil {
		t.Error(err)
	}

	t.Logf("-- second build. output_local=false")
	stats, err = runNinjaTest(t, false, fakere)
	if err != nil {
		t.Fatalf("ninja err: %v", err)
	}
	if stats.Done != stats.Total || stats.Total != 3 || stats.Remote != 1 || stats.Local != 2 {
		t.Errorf("done=%d total=%d remote=%d local=%d; want done=total=3 remote=1 local=2: %#v", stats.Done, stats.Total, stats.Remote, stats.Local, stats)
	}

	for _, name := range []string{"out0", "out1", "out2", "out3"} {
		err = checkFile(dir, filepath.Join("out/siso", name), data)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	t.Logf("-- confirm no op")
	stats, err = runNinjaTest(t, false, fakere)
	if err != nil {
		t.Fatalf("ninja err: %v", err)
	}
	if stats.Done != stats.Total || stats.Total != 3 || stats.Skipped != 3 {
		t.Errorf("done=%d total=%d skipped=%d; want done=total=skipped=3; %#v", stats.Done, stats.Total, stats.Skipped, stats)
	}
}

func TestBuild_OutputLocal_SymlinkOutDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink not available on windows")
	}
	if !runInSubProcess(t) {
		return
	}
	ctx := t.Context()
	dir := tempDir(t)
	outDir := tempDir(t)
	err := os.Symlink(outDir, filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}

	var savedJournal []byte
	runNinjaTest := func(t *testing.T, outputLocal bool, fakere *reapitest.Fake) (build.Stats, error) {
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
		opt, graph, cleanup := setupAndroidBuild(ctx, t, dir, hashfs.Option{
			StateFile:   ".siso_fs_state",
			OutputLocal: func(context.Context, string) bool { return outputLocal },
			DataSource:  ds,
		})
		defer cleanup()
		opt.REAPIClient = ds.Client
		stats, err := ninjabuild.Run(ctx, graph, opt, nil, ninjabuild.RunNinjaOpts{})
		savedJournal, _ = os.ReadFile(filepath.Join(dir, "out/.siso_fs_state.journal"))
		return stats, err
	}
	setupFiles(t, dir, t.Name(), nil)
	fi, err := os.Lstat(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&fs.ModeSymlink != fs.ModeSymlink {
		t.Fatalf("%s is not symlink: %v", filepath.Join(dir, "out"), fi.Mode())
	}

	fakere := &reapitest.Fake{
		ExecuteFunc: func(fakere *reapitest.Fake, action *rpb.Action) (*rpb.ActionResult, error) {
			tree := reapitest.InputTree{CAS: fakere.CAS, Root: action.InputRootDigest}
			fn, err := tree.LookupFileNode(ctx, "input")
			if err != nil {
				t.Logf("missing input: %v", err)
				return &rpb.ActionResult{
					ExitCode:  1,
					StderrRaw: fmt.Appendf(nil, "input: File not found: %v", err),
				}, nil
			}
			return &rpb.ActionResult{
				ExitCode: 0,
				OutputFiles: []*rpb.OutputFile{
					{
						Path:   "out/out0",
						Digest: fn.Digest,
					},
					{
						Path:   "out/out1",
						Digest: fn.Digest,
					},
				},
			}, nil
		},
	}

	checkFile := func(dir, name string, data []byte) error {
		buf, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if diff := cmp.Diff(data, buf); diff != "" {
			return fmt.Errorf("unexpected content for %q: -want +got:\n%s", name, diff)
		}
		return nil
	}

	t.Logf("-- first build. output_local=true")
	stats, err := runNinjaTest(t, true, fakere)
	if err != nil {
		t.Fatalf("ninja err: %v", err)
	}
	if stats.Done != stats.Total || stats.Total != 3 || stats.Remote != 1 || stats.Local != 2 {
		t.Errorf("done=%d total=%d remote=%d local=%d; want done=total=3 remote=1 local=2: %#v", stats.Done, stats.Total, stats.Remote, stats.Local, stats)
	}

	buf, err := os.ReadFile(filepath.Join(dir, "input"))
	if err != nil {
		t.Fatal(err)
	}
	data := buf
	for _, name := range []string{"out0", "out1", "out2", "out3"} {
		err = checkFile(dir, filepath.Join("out", name), data)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		err = checkFile(outDir, name, data)
		if err != nil {
			t.Errorf("outDir/%s: %v", name, err)
		}
	}

	t.Logf("-- confirm no op")
	stats, err = runNinjaTest(t, true, fakere)
	if err != nil {
		t.Fatalf("ninja err: %v", err)
	}
	if stats.Done != stats.Total || stats.Total != 3 || stats.Skipped != 3 {
		t.Errorf("done=%d total=%d skipped=%d; want done=total=skipped=3; %#v", stats.Done, stats.Total, stats.Skipped, stats)
	}

	prevState, err := os.ReadFile(filepath.Join(dir, "out/.siso_fs_state"))
	if err != nil {
		t.Fatal(err)
	}

	data = append(buf, []byte(" modified")...)
	modifyFile(t, dir, "input", func([]byte) []byte { return data })

	err = checkFile(dir, "input", data)
	if err != nil {
		t.Error(err)
	}

	t.Logf("-- second build. output_local=false")
	stats, err = runNinjaTest(t, false, fakere)
	if err != nil {
		t.Fatalf("ninja err: %v", err)
	}
	if stats.Done != stats.Total || stats.Total != 3 || stats.Remote != 1 || stats.Local != 2 {
		t.Errorf("done=%d total=%d remote=%d local=%d; want done=total=3 remote=1 local=2: %#v", stats.Done, stats.Total, stats.Remote, stats.Local, stats)
	}
	secondBuildJournal := savedJournal
	if len(secondBuildJournal) == 0 {
		t.Errorf("expected non-empty .siso_fs_state.journal during second build")
	}
	dec := json.NewDecoder(bytes.NewReader(secondBuildJournal))
	var hdr string
	if err := dec.Decode(&hdr); err != nil {
		t.Fatalf("failed to decode journal header: %v", err)
	}
	var gotJournalNames []string
	for dec.More() {
		var ent pb.Entry
		if err := dec.Decode(&ent); err != nil {
			t.Fatalf("failed to decode journal entry: %v", err)
		}
		gotJournalNames = append(gotJournalNames, ent.Name)
	}
	slices.Sort(gotJournalNames)
	wantJournalNames := []string{
		filepath.ToSlash(filepath.Join(outDir, "out0")),
		filepath.ToSlash(filepath.Join(outDir, "out1")),
		filepath.ToSlash(filepath.Join(outDir, "out2")),
		filepath.ToSlash(filepath.Join(outDir, "out3")),
	}
	slices.Sort(wantJournalNames)
	if diff := cmp.Diff(wantJournalNames, gotJournalNames); diff != "" {
		t.Errorf("journal names diff -want +got:\n%s", diff)
	}

	for _, name := range []string{"out0", "out1", "out2", "out3"} {
		err = checkFile(dir, filepath.Join("out", name), data)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		err = checkFile(outDir, name, data)
		if err != nil {
			t.Errorf("outDir/%s: %v", name, err)
		}
	}

	t.Logf("-- confirm no op")
	stats, err = runNinjaTest(t, false, fakere)
	if err != nil {
		t.Fatalf("ninja err: %v", err)
	}
	if stats.Done != stats.Total || stats.Total != 3 || stats.Skipped != 3 {
		t.Errorf("done=%d total=%d skipped=%d; want done=total=skipped=3; %#v", stats.Done, stats.Total, stats.Skipped, stats)
	}

	t.Logf("-- confirm no op after journal replay")
	err = os.WriteFile(filepath.Join(dir, "out/.siso_fs_state"), prevState, 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(dir, "out/.siso_fs_state.journal"), secondBuildJournal, 0644)
	if err != nil {
		t.Fatal(err)
	}
	stats, err = runNinjaTest(t, false, fakere)
	if err != nil {
		t.Fatalf("ninja err: %v", err)
	}
	if stats.Done != stats.Total || stats.Total != 3 || stats.Skipped != 3 {
		t.Errorf("done=%d total=%d skipped=%d; want done=total=skipped=3; %#v", stats.Done, stats.Total, stats.Skipped, stats)
	}
}

// TestBuild_OutputLocal_StaleDepsTargetCache verifies that when a step with a
// stale .siso_deps entry stats a generated header before its remote or
// remote-cached output_local generator step runs, (*Builder).outputs still
// updates b.targets so downstream consumers of the generated header rebuild
// (b/590907111).
func TestBuild_OutputLocal_StaleDepsTargetCache(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	tname := t.Name()
	for _, tc := range []struct {
		name     string
		cacheHit bool
	}{
		{
			name:     "remote",
			cacheHit: false,
		},
		{
			name:     "remote_cache",
			cacheHit: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			dir := tempDir(t)
			setupFiles(t, dir, tname, nil)

			fakere := &reapitest.Fake{
				ExecuteFunc: func(fakere *reapitest.Fake, action *rpb.Action) (*rpb.ActionResult, error) {
					tree := reapitest.InputTree{CAS: fakere.CAS, Root: action.InputRootDigest}
					fn, err := tree.LookupFileNode(ctx, "foo.in")
					if err != nil {
						t.Logf("missing input: %v", err)
						return &rpb.ActionResult{
							ExitCode:  1,
							StderrRaw: fmt.Appendf(nil, "../../foo.in: File not found: %v", err),
						}, nil
					}
					return &rpb.ActionResult{
						ExitCode: 0,
						OutputFiles: []*rpb.OutputFile{
							{
								Path:   "gen/foo.h",
								Digest: fn.Digest,
							},
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

			runNinjaTest := func(t *testing.T, workDir string, cacheRead bool, targets []string) (build.Stats, error) {
				t.Helper()
				opt, graph, cleanup := setupBuild(ctx, t, workDir, hashfs.Option{
					StateFile:  ".siso_fs_state",
					DataSource: ds,
				})
				defer cleanup()
				if cacheRead {
					bcache, err := build.NewCache(ctx, build.CacheOptions{
						Store:      ds.Cache,
						EnableRead: true,
					})
					if err != nil {
						return build.Stats{}, err
					}
					opt.Cache = bcache
					opt.RECacheEnableRead = true
				}
				opt.REAPIClient = ds.Client
				opt.REExecEnable = true
				// Serialize steps so scheduling order is deterministic by
				// critical-path weight: a.o (weight 3) runs before gen/foo.h
				// (weight 2), which runs before b.o (weight 1).
				opt.Limits.Step = 1
				return ninjabuild.Run(ctx, graph, opt, targets, ninjabuild.RunNinjaOpts{})
			}

			t.Logf("-- first build")
			stats, err := runNinjaTest(t, dir, tc.cacheHit, nil)
			if err != nil {
				t.Fatalf("ninja err: %v", err)
			}
			if stats.Done != stats.Total || stats.Total != 6 || stats.Remote != 1 || stats.CacheHit != 0 || stats.Local != 4 {
				t.Fatalf("first build: done=%d total=%d remote=%d cache_hit=%d local=%d; want done=total=6 remote=1 cache_hit=0 local=4: %#v",
					stats.Done, stats.Total, stats.Remote, stats.CacheHit, stats.Local, stats)
			}

			// Simulate an incremental change where:
			// 1. a.cc drops its include of gen/foo.h and its order-only dep on
			//    gen/foo.h, leaving a stale gen/foo.h entry in .siso_deps for a.o.
			// 2. foo.in is modified so gen/foo.h is regenerated remotely (or
			//    fetched from the remote action cache) with output_local: True.
			// 3. b.o still depends on gen/foo.h and must be rebuilt.
			modifyFile(t, dir, "foo.in", func([]byte) []byte {
				return []byte("// foo v2\n")
			})
			modifyFile(t, dir, "a.cc", func([]byte) []byte {
				return []byte("// a v2 (no longer includes gen/foo.h)\n")
			})
			modifyFile(t, dir, "out/siso/build.ninja", func(buf []byte) []byte {
				return bytes.ReplaceAll(buf,
					[]byte("build a.o: cxx ../../a.cc || gen/foo.h"),
					[]byte("build a.o: cxx ../../a.cc"),
				)
			})

			if tc.cacheHit {
				t.Logf("-- warm remote action cache for foo.in v2")
				warmDir := tempDir(t)
				setupFiles(t, warmDir, tname, nil)
				if err := os.WriteFile(filepath.Join(warmDir, "foo.in"), []byte("// foo v2\n"), 0644); err != nil {
					t.Fatal(err)
				}
				warmStats, err := runNinjaTest(t, warmDir, true, []string{"gen/foo.h"})
				if err != nil {
					t.Fatalf("warm build err: %v", err)
				}
				if warmStats.Remote != 1 || warmStats.CacheHit != 0 {
					t.Fatalf("warm build: remote=%d cache_hit=%d; want remote=1 cache_hit=0: %#v",
						warmStats.Remote, warmStats.CacheHit, warmStats)
				}
			}

			t.Logf("-- second build")
			stats, err = runNinjaTest(t, dir, tc.cacheHit, nil)
			if err != nil {
				t.Fatalf("ninja err: %v", err)
			}
			wantRemote, wantCacheHit := 1, 0
			if tc.cacheHit {
				wantRemote, wantCacheHit = 0, 1
			}
			if stats.Done != stats.Total || stats.Total != 6 || stats.Remote != wantRemote || stats.CacheHit != wantCacheHit || stats.Local != 4 || stats.Skipped != 1 {
				t.Errorf("second build: done=%d total=%d remote=%d cache_hit=%d local=%d skipped=%d; want done=total=6 remote=%d cache_hit=%d local=4 skipped=1: %#v",
					stats.Done, stats.Total, stats.Remote, stats.CacheHit, stats.Local, stats.Skipped, wantRemote, wantCacheHit, stats)
			}
			if got := readFile(t, filepath.Join(dir, "out/siso/b.o")); !strings.Contains(got, "// foo v2") {
				t.Errorf("out/siso/b.o = %q; want to contain %q", got, "// foo v2")
			}

			t.Logf("-- confirm no-op after second build")
			stats, err = runNinjaTest(t, dir, tc.cacheHit, nil)
			if err != nil {
				t.Fatalf("ninja err: %v", err)
			}
			if stats.Done != stats.Total || stats.Total != 6 || stats.Skipped != 6 || stats.Local != 0 || stats.Remote != 0 || stats.CacheHit != 0 {
				t.Errorf("confirm no-op: done=%d total=%d skipped=%d local=%d remote=%d cache_hit=%d; want done=total=skipped=6 local=0 remote=0 cache_hit=0: %#v",
					stats.Done, stats.Total, stats.Skipped, stats.Local, stats.Remote, stats.CacheHit, stats)
			}
		})
	}
}
