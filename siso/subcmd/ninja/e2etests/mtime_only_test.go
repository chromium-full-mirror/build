// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/hashfs"
	pb "go.chromium.org/build/siso/hashfs/proto"
	"go.chromium.org/build/siso/subcmd/ninja"
	"go.chromium.org/build/siso/ui"
)

func setupMtimeOnlyWorkspace(t *testing.T, dir string) {
	t.Helper()
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		past := time.Now().Add(-2 * time.Second)
		if err := os.Chtimes(full, past, past); err != nil {
			t.Fatal(err)
		}
	}

	write("build/config/siso/main.star", `load("@builtin//encoding.star", "json")
load("@builtin//struct.star", "module")

def init(ctx):
    return module(
        "config",
        step_config = json.encode({}),
        filegroups = {},
        handlers = {},
    )
`)
	write("cp.py", `import shutil, sys
shutil.copyfile(sys.argv[1], sys.argv[2])
`)
	write("in/src.txt", "v1\n")
	write("out/siso/build.ninja", `rule cp
  command = python3 ../../cp.py ${in} ${out}

build out: cp ../../in/src.txt | ../../cp.py
build all: phony out
build build.ninja: phony
`)
}

func runNinjaCmdForMtimeOnly(t *testing.T, dir string, extraArgs ...string) (build.Stats, error) {
	t.Helper()
	ctx := t.Context()
	t.Chdir(dir)

	cmd := ninja.Command{}
	flagSet := flag.NewFlagSet("ninja", flag.ContinueOnError)
	cmd.SetFlags(flagSet)
	cmd.Flags = flagSet
	args := []string{"-C", "out/siso", "--offline", "-fast_nop"}
	args = append(args, extraArgs...)
	if err := flagSet.Parse(args); err != nil {
		t.Fatal(err)
	}
	return cmd.Run(ctx)
}

// TestBuild_MtimeOnly_FastNop verifies end-to-end that mtime-only inputs
// from a clean build are persisted as files without digest so the very first
// subsequent ninja invocation hits the fast-nop shortcut without loading
// build.ninja, while states written by older siso (names only in
// missing_digests) stay conservative and source modifications or deletions
// still invalidate fast-nop.
func TestBuild_MtimeOnly_FastNop(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	requirePython3(t)
	ctx := t.Context()
	dir := tempDir(t)

	uiDefault := ui.Default
	ui.Default = &ui.TermUI{}
	defer func() {
		ui.Default = uiDefault
	}()

	setupMtimeOnlyWorkspace(t, dir)
	statePath := filepath.Join(dir, "out/siso/.siso_fs_state")
	srcFull := filepath.ToSlash(filepath.Join(dir, "in/src.txt"))

	// 1. Clean build: local command reads in/src.txt from OS disk without
	// computing a HashFS digest, so in/src.txt is tracked by mtime only.
	stats, err := runNinjaCmdForMtimeOnly(t, dir, "all")
	if err != nil {
		t.Fatalf("build 1 (clean): %v", err)
	}
	if got, want := stats.Local, 1; got != want {
		t.Errorf("build 1 stats.Local = %d; want %d (%#v)", got, want, stats)
	}
	if got, want := readFile(t, filepath.Join(dir, "out/siso/out")), "v1\n"; got != want {
		t.Fatalf("out/siso/out = %q; want %q", got, want)
	}

	st, err := hashfs.Load(ctx, hashfs.Option{StateFile: statePath})
	if err != nil {
		t.Fatalf("hashfs.Load: %v", err)
	}
	i := slices.IndexFunc(st.Entries, func(ent *pb.Entry) bool { return ent.Name == srcFull })
	if i < 0 {
		t.Fatalf("st.Entries has no %q", srcFull)
	}
	if ent := st.Entries[i]; ent.Kind != pb.Entry_KIND_FILE || ent.Digest != nil || ent.Size != int64(len("v1\n")) {
		t.Errorf("st.Entries[%q] = %v; want KIND_FILE without digest, size=%d", srcFull, ent, len("v1\n"))
	}
	if slices.Contains(st.MissingDigests, srcFull) {
		t.Errorf("st.MissingDigests = %q; want no %q", st.MissingDigests, srcFull)
	}

	// Save a copy of the clean-build state for the cases below.
	cleanStateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Immediate first no-op build: must hit fast-nop before loading
	// build.ninja (stats.Total == 0 and "nothing to do" sentinel).
	stats, err = runNinjaCmdForMtimeOnly(t, dir, "all")
	if err == nil || err.Error() != "nothing to do" {
		t.Fatalf("build 2 (first no-op) err = %v (stats=%#v); want fast-nop 'nothing to do'", err, stats)
	}
	if got, want := stats.Total, 0; got != want {
		t.Errorf("build 2 stats.Total = %d; want %d (manifest load skipped)", got, want)
	}

	// 3. Older siso saved the file only in missing_digests, so reload must
	// stay conservative (no fast-nop), calculate digests, and converge so the
	// following run fast-nops.
	if err := os.WriteFile(statePath, cleanStateBytes, 0644); err != nil {
		t.Fatal(err)
	}
	oldSt, err := hashfs.Load(ctx, hashfs.Option{StateFile: statePath})
	if err != nil {
		t.Fatal(err)
	}
	oldSt.Entries = slices.DeleteFunc(oldSt.Entries, func(ent *pb.Entry) bool { return ent.Name == srcFull })
	oldSt.MissingDigests = append(oldSt.MissingDigests, srcFull)
	if err := hashfs.Save(ctx, oldSt, hashfs.Option{StateFile: statePath}); err != nil {
		t.Fatal(err)
	}

	stats, err = runNinjaCmdForMtimeOnly(t, dir, "all")
	if err != nil {
		t.Fatalf("build 3 (old-format state) err = %v; want nil (full graph walk, not fast-nop)", err)
	}
	if stats.Total == 0 || stats.Skipped != stats.Total {
		t.Errorf("build 3 stats = %#v; want Total > 0 and Skipped == Total", stats)
	}

	stats, err = runNinjaCmdForMtimeOnly(t, dir, "all")
	if err == nil || err.Error() != "nothing to do" {
		t.Fatalf("build 3b (no-op after converging from older format) err = %v (stats=%#v); want fast-nop 'nothing to do'", err, stats)
	}
	if got, want := stats.Total, 0; got != want {
		t.Errorf("build 3b stats.Total = %d; want %d", got, want)
	}

	// 4. Restore the clean-build state, modify in/src.txt, and verify fast-nop
	// is invalidated so the output is rebuilt, then fast-nops again.
	if err := os.WriteFile(statePath, cleanStateBytes, 0644); err != nil {
		t.Fatal(err)
	}
	modifyFile(t, dir, "in/src.txt", func([]byte) []byte {
		return []byte("v2\n")
	})
	stats, err = runNinjaCmdForMtimeOnly(t, dir, "all")
	if err != nil {
		t.Fatalf("build 4 (after modifying source): %v", err)
	}
	if got, want := stats.Local, 1; got != want {
		t.Errorf("build 4 stats.Local = %d; want %d (%#v)", got, want, stats)
	}
	if got, want := readFile(t, filepath.Join(dir, "out/siso/out")), "v2\n"; got != want {
		t.Fatalf("out/siso/out after modify = %q; want %q", got, want)
	}

	stats, err = runNinjaCmdForMtimeOnly(t, dir, "all")
	if err == nil || err.Error() != "nothing to do" {
		t.Fatalf("build 5 (no-op after incremental rebuild) err = %v (stats=%#v); want fast-nop", err, stats)
	}
	if got, want := stats.Total, 0; got != want {
		t.Errorf("build 5 stats.Total = %d; want %d", got, want)
	}

	// 5. Removing the mtime-only source must invalidate fast-nop and fail with
	// MissingSourceError rather than falsely reporting nothing to do.
	if err := os.Remove(filepath.Join(dir, "in/src.txt")); err != nil {
		t.Fatal(err)
	}
	_, err = runNinjaCmdForMtimeOnly(t, dir, "all")
	if err == nil {
		t.Fatalf("build 6 (after removing source) succeeded; want MissingSourceError")
	}
	if _, ok := errors.AsType[build.MissingSourceError](err); !ok {
		t.Errorf("build 6 err = %v; want build.MissingSourceError", err)
	}
}

// TestBuild_MtimeOnly_DeferDigest verifies fast-nop behavior under
// -fs_defer_digest (where Close quits early before background digests drain and
// SetState skips triggerDigestCalculation).
func TestBuild_MtimeOnly_DeferDigest(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	requirePython3(t)
	dir := tempDir(t)

	uiDefault := ui.Default
	ui.Default = &ui.TermUI{}
	defer func() {
		ui.Default = uiDefault
	}()

	setupMtimeOnlyWorkspace(t, dir)

	// Clean build with -fs_defer_digest.
	stats, err := runNinjaCmdForMtimeOnly(t, dir, "-fs_defer_digest", "all")
	if err != nil {
		t.Fatalf("defer build 1 (clean): %v", err)
	}
	if got, want := stats.Local, 1; got != want {
		t.Errorf("defer build 1 stats.Local = %d; want %d (%#v)", got, want, stats)
	}

	// Immediate first no-op under -fs_defer_digest must hit fast-nop.
	stats, err = runNinjaCmdForMtimeOnly(t, dir, "-fs_defer_digest", "all")
	if err == nil || err.Error() != "nothing to do" {
		t.Fatalf("defer build 2 (first no-op) err = %v (stats=%#v); want fast-nop 'nothing to do'", err, stats)
	}
	if got, want := stats.Total, 0; got != want {
		t.Errorf("defer build 2 stats.Total = %d; want %d", got, want)
	}

	// Modifying source under -fs_defer_digest must rebuild and then fast-nop again.
	modifyFile(t, dir, "in/src.txt", func([]byte) []byte {
		return []byte("v2-defer\n")
	})
	stats, err = runNinjaCmdForMtimeOnly(t, dir, "-fs_defer_digest", "all")
	if err != nil {
		t.Fatalf("defer build 3 (after modify): %v", err)
	}
	if got, want := stats.Local, 1; got != want {
		t.Errorf("defer build 3 stats.Local = %d; want %d (%#v)", got, want, stats)
	}
	if got, want := readFile(t, filepath.Join(dir, "out/siso/out")), "v2-defer\n"; got != want {
		t.Fatalf("out/siso/out = %q; want %q", got, want)
	}

	stats, err = runNinjaCmdForMtimeOnly(t, dir, "-fs_defer_digest", "all")
	if err == nil || err.Error() != "nothing to do" {
		t.Fatalf("defer build 4 (no-op after rebuild) err = %v (stats=%#v); want fast-nop", err, stats)
	}
}
