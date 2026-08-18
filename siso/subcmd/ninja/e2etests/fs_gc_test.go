// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/subcommands"

	"go.chromium.org/build/siso/auth/cred"
	"go.chromium.org/build/siso/build/ninjabuild"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/subcmd/fscmd"
)

const stateFileName = ".siso_fs_state"

func setupGCTestDir(t *testing.T) (context.Context, string) {
	t.Helper()
	ctx := t.Context()
	dir := tempDir(t)

	if err := os.MkdirAll(filepath.Join(dir, "build/config/siso"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "out/siso"), 0755); err != nil {
		t.Fatal(err)
	}

	setupFiles(t, dir, "TestBuild_FSGC_E2E", nil)
	if err := os.Remove(filepath.Join(dir, "out/siso", stateFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return ctx, dir
}

func runNinjaWithLabel(ctx context.Context, t *testing.T, dir, label, ninjaFile string) {
	t.Helper()
	original := filepath.Join(dir, "out/siso", ninjaFile)
	dest := filepath.Join(dir, "out/siso/build.ninja")
	originalB, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, originalB, 0644); err != nil {
		t.Fatal(err)
	}

	opt, graph, cleanup := setupBuild(ctx, t, dir, hashfs.Option{StateFile: stateFileName, BuildLabel: label})
	defer cleanup()

	_, err = ninjabuild.Run(ctx, graph, opt, nil, ninjabuild.RunNinjaOpts{})
	if err != nil {
		t.Fatalf("ninja err for %s, got=%v; want=<nil>", label, err)
	}
}

func runGC(ctx context.Context, t *testing.T, dir string, args ...string) error {
	t.Helper()
	cmd := fscmd.Cmd(cred.Options{})
	fs := flag.NewFlagSet("fs", flag.ContinueOnError)
	cmd.SetFlags(fs)
	fullArgs := append([]string{"gc", "-C", filepath.Join(dir, "out/siso"), "--fs_state", stateFileName}, args...)
	if err := fs.Parse(fullArgs); err != nil {
		return err
	}
	status := cmd.Execute(ctx, fs)
	if status != subcommands.ExitSuccess {
		return fmt.Errorf("fscmd gc exited with status %v", status)
	}
	return nil
}

func TestBuild_FSGC_EvictTarget(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx, dir := setupGCTestDir(t)

	runNinjaWithLabel(ctx, t, dir, "target-1", "build.ninja.0")
	for _, fname := range []string{"out/siso/obj/bar.o", "out/siso/obj/foo.o"} {
		if _, err := os.Stat(filepath.Join(dir, fname)); err != nil {
			t.Fatalf("stat(%s) err, got=%v; want=<nil>", fname, err)
		}
	}

	time.Sleep(50 * time.Millisecond)

	runNinjaWithLabel(ctx, t, dir, "target-2", "build.ninja.1")
	for _, fname := range []string{"out/siso/obj/bar.o", "out/siso/obj/foo.o", "out/siso/obj/baz.o"} {
		if _, err := os.Stat(filepath.Join(dir, fname)); err != nil {
			t.Fatalf("stat(%s) err, got=%v; want=<nil>", fname, err)
		}
	}

	err := runGC(ctx, t, dir, "target-1")
	if err != nil {
		t.Fatalf("runGC err, got=%v; want=<nil>", err)
	}

	// Validate obj/bar.o is physically deleted (exclusive to target-1)
	if _, err := os.Stat(filepath.Join(dir, "out/siso/obj/bar.o")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat(obj/bar.o) err, got=%v; want=os.ErrNotExist", err)
	}
	// Validate obj/foo.o remains intact (preventing accidental shared cache purges - used by target-2)
	if _, err := os.Stat(filepath.Join(dir, "out/siso/obj/foo.o")); err != nil {
		t.Errorf("stat(obj/foo.o) err, got=%v; want=<nil>", err)
	}
}

func TestBuild_FSGC_RetainLastX(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx, dir := setupGCTestDir(t)

	runNinjaWithLabel(ctx, t, dir, "target-1", "build.ninja.0")
	time.Sleep(50 * time.Millisecond)
	runNinjaWithLabel(ctx, t, dir, "target-2", "build.ninja.1")

	err := runGC(ctx, t, dir, "--retain_last_x=1")
	if err != nil {
		t.Fatalf("runGC err, got=%v; want=<nil>", err)
	}

	// Older targets (target-1) fall out, exclusively owned artifacts are evicted.
	if _, err := os.Stat(filepath.Join(dir, "out/siso/obj/bar.o")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat(obj/bar.o) err, got=%v; want=os.ErrNotExist", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out/siso/obj/foo.o")); err != nil {
		t.Errorf("stat(obj/foo.o) err, got=%v; want=<nil>", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out/siso/obj/baz.o")); err != nil {
		t.Errorf("stat(obj/baz.o) err, got=%v; want=<nil>", err)
	}
}

func TestBuild_FSGC_DryRun(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx, dir := setupGCTestDir(t)

	runNinjaWithLabel(ctx, t, dir, "target-1", "build.ninja.0")

	outputFile := filepath.Join(dir, "evicted.txt")
	err := runGC(ctx, t, dir, "--dry_run", "--output_file", outputFile, "target-1")
	if err != nil {
		t.Fatalf("runGC err, got=%v; want=<nil>", err)
	}

	evictedFilesContent, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) err, got=%v; want=<nil>", outputFile, err)
	}
	evictedFiles := strings.Split(strings.TrimSpace(string(evictedFilesContent)), "\n")
	if len(evictedFiles) != 7 {
		t.Errorf("len(evictedFiles), got=%d; want=7", len(evictedFiles))
	}

	// Confirm zero files were dropped, obj/foo.o and obj/bar.o still exist
	if _, err := os.Stat(filepath.Join(dir, "out/siso/obj/foo.o")); err != nil {
		t.Errorf("stat(obj/foo.o) dry run err, got=%v; want=<nil>", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out/siso/obj/bar.o")); err != nil {
		t.Errorf("stat(obj/bar.o) dry run err, got=%v; want=<nil>", err)
	}
}

func TestBuild_FSGC_ValidationErrors(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx, dir := setupGCTestDir(t)

	t.Run("NoStrategy", func(t *testing.T) {
		err := runGC(ctx, t, dir)
		if err == nil {
			t.Errorf("runGC empty strategy err, got=<nil>; want=error")
		}
	})

	t.Run("ConflictingStrategies", func(t *testing.T) {
		err := runGC(ctx, t, dir, "--retain_last_x=1", "target-1")
		if err == nil {
			t.Errorf("runGC conflicting strategy err, got=<nil>; want=error")
		}
	})
}

func TestBuild_FSGC_ListActiveLabels(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx, dir := setupGCTestDir(t)

	// Seed the HashFS state ledger with actual builds
	runNinjaWithLabel(ctx, t, dir, "target-1", "build.ninja.0")
	time.Sleep(50 * time.Millisecond)
	runNinjaWithLabel(ctx, t, dir, "target-2", "build.ninja.1")

	// Capture stdout asynchronously
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() {
		os.Stdout = oldStdout
		w.Close() // Ensure restoration and unblocking if runGC panics
	}()

	errCh := make(chan error, 1)
	var outBytes []byte
	go func() {
		var err error
		outBytes, err = io.ReadAll(r)
		errCh <- err
	}()

	err := runGC(ctx, t, dir, "--list_active_labels")

	w.Close()

	if readErr := <-errCh; readErr != nil {
		t.Fatalf("Failed to read stdout pipe: %v", readErr)
	}

	if err != nil {
		t.Fatalf("runGC err, got=%v; want=<nil>", err)
	}

	output := string(outBytes)

	if !strings.Contains(output, "Active Build Labels (2):") {
		t.Errorf("output missing summary header, got:\n%s", output)
	}
	if !strings.Contains(output, " - target-1") {
		t.Errorf("output missing target-1, got:\n%s", output)
	}
	if !strings.Contains(output, " - target-2") {
		t.Errorf("output missing target-2, got:\n%s", output)
	}

	// Confirm zero files were dropped, obj/foo.o and obj/bar.o still exist
	if _, err := os.Stat(filepath.Join(dir, "out/siso/obj/foo.o")); err != nil {
		t.Errorf("stat(obj/foo.o) err, got=%v; want=<nil>", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out/siso/obj/bar.o")); err != nil {
		t.Errorf("stat(obj/bar.o) err, got=%v; want=<nil>", err)
	}
}
