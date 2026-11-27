// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/ninjabuild"
	"go.chromium.org/build/siso/execute/localexec"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/reapi"
	"go.chromium.org/build/siso/reapi/reapitest"
)

const edgeHashOutputTestdata = "TestBuild_EdgeHashOutput"

// stateEdgeHash returns the edge hash that the state file in dir holds for
// the output out/siso/name.
func stateEdgeHash(ctx context.Context, t *testing.T, dir, name string) []byte {
	t.Helper()
	st, err := hashfs.Load(ctx, hashfs.Option{StateFile: filepath.Join(dir, "out/siso/.siso_fs_state")})
	if err != nil {
		t.Fatalf("hashfs.Load: %v", err)
	}
	ent, ok := hashfs.StateMap(digest.SHA256, st)[filepath.ToSlash(filepath.Join(dir, "out/siso", name))]
	if !ok {
		t.Fatalf("%s is not in the state", name)
	}
	return ent.GetEdgeHash()
}

func writeBuildNinja(t *testing.T, dir, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, "out/siso/build.ninja"), []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}
}

// TestBuild_EdgeChange_DirOutputFirst checks that a step whose first output
// is a directory runs again when only its build statement changes.
func TestBuild_EdgeChange_DirOutputFirst(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	requirePython3(t)
	ctx := t.Context()
	dir := tempDir(t)

	runNinja := func(t *testing.T) build.Stats {
		t.Helper()
		opt, graph, cleanup := setupBuild(ctx, t, dir, hashfs.Option{
			StateFile: ".siso_fs_state",
		})
		defer cleanup()
		stats, err := ninjabuild.Run(ctx, graph, opt, []string{"all"}, ninjabuild.RunNinjaOpts{})
		if err != nil {
			t.Fatalf("ninja %v", err)
		}
		return stats
	}

	setupFiles(t, dir, edgeHashOutputTestdata, nil)
	const buildNinja = `
rule gen
  command = python3 ../../tools/gen.py --mkdir=z-dir --output=z-dir/data --output=a.stamp ../../base/in1

build z-dir/ a.stamp: gen | ../../base/in1 ../../base/in2
build all: phony z-dir/ a.stamp
build build.ninja: phony
`
	writeBuildNinja(t, dir, buildNinja)

	t.Logf("-- first build")
	if stats := runNinja(t); stats.Local != 1 {
		t.Fatalf("Local=%d; want 1", stats.Local)
	}

	t.Logf("-- second build: in2 is no longer an input")
	writeBuildNinja(t, dir, strings.Replace(buildNinja, " ../../base/in2", "", 1))
	if stats := runNinja(t); stats.Local != 1 {
		t.Errorf("Local=%d; want 1", stats.Local)
	}

	t.Logf("-- third build: no change")
	if stats := runNinja(t); stats.Skipped != stats.Total {
		t.Errorf("Skipped=%d Total=%d; want all skipped", stats.Skipped, stats.Total)
	}
}

// TestBuild_EdgeChange_FirstOutputChanged checks that a step reruns when an
// edit changes which declared output is first, so the new first output has a
// command hash from the previous build but no edge hash.
func TestBuild_EdgeChange_FirstOutputChanged(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	requirePython3(t)
	const (
		mkFiles = "--mkdir=z-dir --output=a.stamp --output=b.stamp"
		mkDir   = "--mkdir=z-dir --output=z-dir/data --output=a.stamp"
	)
	for _, tc := range []struct {
		name, mk, outs1, outs2 string
	}{
		{name: "swap_files", mk: mkFiles, outs1: "a.stamp b.stamp", outs2: "b.stamp a.stamp"},
		{name: "swap_dir_and_file", mk: mkDir, outs1: "z-dir/ a.stamp", outs2: "a.stamp z-dir/"},
		{name: "drop_first_output", mk: mkFiles, outs1: "a.stamp b.stamp", outs2: "b.stamp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			dir := tempDir(t)
			setupFiles(t, dir, edgeHashOutputTestdata, nil)

			runNinja := func(t *testing.T) build.Stats {
				t.Helper()
				opt, graph, cleanup := setupBuild(ctx, t, dir, hashfs.Option{
					StateFile: ".siso_fs_state",
				})
				defer cleanup()
				stats, err := ninjabuild.Run(ctx, graph, opt, []string{"all"}, ninjabuild.RunNinjaOpts{})
				if err != nil {
					t.Fatalf("ninja %v", err)
				}
				return stats
			}
			manifest := func(outs, ins string) string {
				return `
rule gen
  command = python3 ../../tools/gen.py ` + tc.mk + ` ../../base/in1

build ` + outs + `: gen | ` + ins + `
build all: phony ` + outs + `
build build.ninja: phony
`
			}

			writeBuildNinja(t, dir, manifest(tc.outs1, "../../base/in1 ../../base/in2"))
			if stats := runNinja(t); stats.Local != 1 {
				t.Fatalf("first build Local=%d; want 1", stats.Local)
			}

			writeBuildNinja(t, dir, manifest(tc.outs2, "../../base/in1"))
			if stats := runNinja(t); stats.Local != 1 {
				t.Errorf("second build Local=%d; want 1", stats.Local)
			}

			if stats := runNinja(t); stats.Skipped != stats.Total {
				t.Errorf("third build Skipped=%d Total=%d; want all skipped", stats.Skipped, stats.Total)
			}
		})
	}
}

// TestBuild_EdgeHashOutput_RuleOutput checks that an output that a Siso rule
// adds doesn't take the edge hash from the step's declared output, also when
// -output_local_strategy=graph applies the rule before the step runs.
func TestBuild_EdgeHashOutput_RuleOutput(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	requirePython3(t)
	for _, strategy := range []string{"full", "graph"} {
		t.Run(strategy, func(t *testing.T) {
			ctx := t.Context()
			dir := tempDir(t)

			setupFiles(t, dir, edgeHashOutputTestdata, nil)
			modifyFile(t, dir, "build/config/siso/main.star", func(b []byte) []byte {
				return bytes.Replace(b, []byte(`"rules": [],`), []byte(`"rules": [{"name": "gen", "action": "gen", "outputs": ["./a.stamp"]}],`), 1)
			})
			writeBuildNinja(t, dir, `
rule gen
  command = python3 ../../tools/gen.py --mkdir=z-dir --output=z-dir/data --output=a.stamp ../../base/in1

build z-dir/: gen ../../base/in1
build all: phony z-dir/
build build.ninja: phony
`)

			func() {
				fsopt := hashfs.Option{StateFile: ".siso_fs_state"}
				var locallyNeeded *build.LocallyNeededSet
				if strategy == "graph" {
					locallyNeeded = build.NewLocallyNeededSet()
					fsopt.OutputLocal = locallyNeeded.OutputLocal
				}
				opt, graph, cleanup := setupBuild(ctx, t, dir, fsopt)
				defer cleanup()
				opt.LocallyNeeded = locallyNeeded
				stats, err := ninjabuild.Run(ctx, graph, opt, []string{"all"}, ninjabuild.RunNinjaOpts{})
				if err != nil {
					t.Fatalf("ninja %v", err)
				}
				if stats.Local != 1 {
					t.Fatalf("Local=%d; want 1", stats.Local)
				}
			}()

			if got := stateEdgeHash(ctx, t, dir, "z-dir"); len(got) == 0 {
				t.Errorf("z-dir has no edge hash")
			}
			if got := stateEdgeHash(ctx, t, dir, "a.stamp"); len(got) != 0 {
				t.Errorf("a.stamp has edge hash %x; want none", got)
			}
		})
	}
}

// TestBuild_TwoPhaseCaching_DirOutputEdgeHash checks that a two phase cache
// hit keeps the edge hash on the step's declared directory, although the
// cached Command also lists a file that tap discovered.
func TestBuild_TwoPhaseCaching_DirOutputEdgeHash(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("two phase caching is available on linux only")
	}
	if !runInSubProcess(t) {
		return
	}
	requirePython3(t)
	ctx := t.Context()
	dir := tempDir(t)
	t.Setenv("TMPDIR", tempDir(t))
	t.Setenv("TMP", tempDir(t))

	build.SetExperimentForTest("two-phase-caching")

	fakere := &reapitest.Fake{}
	var ds build.DataSource
	defer func() {
		err := ds.Close(ctx)
		if err != nil {
			t.Error(err)
		}
	}()
	localCache, err := reapi.NewLocalCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ds.Client = reapitest.NewWithOption(ctx, t, fakere, reapi.Option{
		LocalCache:                    localCache,
		DisableTwoPhaseCachingMethods: true,
	})
	err = ds.Client.Init(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ds.Cache = ds.Client.CacheStore()

	runNinja := func(t *testing.T) build.Stats {
		t.Helper()
		opt, graph, cleanup := setupBuild(ctx, t, dir, hashfs.Option{
			StateFile:   ".siso_fs_state",
			OutputLocal: func(context.Context, string) bool { return true },
			DataSource:  ds,
		})
		defer cleanup()
		bcache, err := build.NewCache(ctx, build.CacheOptions{
			Store:      ds.Cache,
			EnableRead: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		opt.Cache = bcache
		opt.RECacheEnableRead = true
		opt.RECacheEnableWrite = true
		opt.REAPIClient = ds.Client
		opt.REExecEnable = true
		stats, err := ninjabuild.Run(ctx, graph, opt, []string{"all"}, ninjabuild.RunNinjaOpts{})
		if err != nil {
			t.Fatalf("ninja err: %v", err)
		}
		return stats
	}

	setupFiles(t, dir, edgeHashOutputTestdata, nil)
	writeBuildNinja(t, dir, `
rule gen
  command = python3 ../../tools/gen.py --mkdir=z-dir --output=z-dir/data --output=a.txt ../../base/in1

build z-dir/: gen ../../base/in1
build all: phony z-dir/
build build.ninja: phony
`)
	origHelper := localexec.SetSpawnHelper(mockTapHelper{})
	defer localexec.SetSpawnHelper(origHelper)

	t.Logf("--- first build: run gen locally and write the 2PC cache")
	if stats := runNinja(t); stats.Local != 1 {
		t.Fatalf("Local=%d; want 1", stats.Local)
	}
	want := stateEdgeHash(ctx, t, dir, "z-dir")
	if len(want) == 0 {
		t.Fatalf("z-dir has no edge hash after the local build")
	}

	t.Logf("--- second build: clean outputs and state, expect a 2PC hit")
	for _, p := range []string{"z-dir", "a.txt", ".siso_fs_state"} {
		if err := os.RemoveAll(filepath.Join(dir, "out/siso", p)); err != nil {
			t.Fatal(err)
		}
	}
	if stats := runNinja(t); stats.TwoPhaseCacheHit != 1 {
		t.Fatalf("TwoPhaseCacheHit=%d; want 1", stats.TwoPhaseCacheHit)
	}
	if got := stateEdgeHash(ctx, t, dir, "z-dir"); !bytes.Equal(got, want) {
		t.Errorf("edge hash of z-dir after the 2PC hit=%x; want %x", got, want)
	}

	t.Logf("--- third build: no change")
	if stats := runNinja(t); stats.Skipped != stats.Total {
		t.Errorf("Skipped=%d Total=%d; want all skipped", stats.Skipped, stats.Total)
	}
}
