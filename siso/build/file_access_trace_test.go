// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/anypb"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/execute"
	epb "go.chromium.org/build/siso/execute/proto"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
)

// TestFilesDiff_DirTargetCoversDescendants verifies filesDiff treats files
// traced under a declared directory artifact as expected, not as undeclared
// extras. A directory is declared as one unit but the syscall trace reports
// each descendant; without directory awareness every descendant becomes an add
// and the directory a del, marking a pure directory-I/O step impure.
func TestFilesDiff_DirTargetCoversDescendants(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	hfs, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hfs.Close(ctx)
	if err := hfs.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}

	// Traced paths are absolute (as siso-tap reports them) or workdir-relative;
	// declared targets are workspace-relative (as cmd.AllOutputs/AllInputs yield them).
	for _, rel := range []string{
		"out/siso/gen/extracted/a.h",
		"out/siso/gen/extracted/sub/b.h",
		"out/siso/other/stray.h",
	} {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	b := &Builder{hashFS: hfs, path: NewPath(dir, "out/siso")}
	traced := []string{
		filepath.Join(dir, "out/siso/gen/extracted/a.h"),
		filepath.Join(dir, "out/siso/gen/extracted/sub/b.h"),
	}

	// A directory output is recorded slash-stripped (cmd.OutputDirs); a
	// directory input keeps its trailing slash (cmd.Inputs). Both must cover
	// their descendants.
	for _, tc := range []struct {
		name     string
		declared []string
	}{
		{name: "output_slash_stripped", declared: []string{"out/siso/gen/extracted"}},
		{name: "input_trailing_slash", declared: []string{"out/siso/gen/extracted/"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adds, dels, _, errs := filesDiff(ctx, b, tc.declared, nil, traced, "")
			if len(errs) != 0 {
				t.Fatalf("filesDiff errs: %v", errs)
			}
			if len(adds) != 0 {
				t.Errorf("adds = %v; want none (descendants of a declared directory are expected, not extra)", adds)
			}
			if len(dels) != 0 {
				t.Errorf("dels = %v; want none (the declared directory is covered by its traced descendants)", dels)
			}
		})
	}

	// An unrelated traced file outside any declared target must still be
	// flagged, so the directory-coverage fix does not suppress real findings.
	adds, _, _, errs := filesDiff(ctx, b, []string{"out/siso/gen/extracted"}, nil,
		append(slices.Clone(traced), filepath.Join(dir, "out/siso/other/stray.h")), "")
	if len(errs) != 0 {
		t.Fatalf("filesDiff errs: %v", errs)
	}
	if !slices.Contains(adds, "out/siso/other/stray.h") {
		t.Errorf("adds = %v; want it to include out/siso/other/stray.h (a genuinely undeclared access)", adds)
	}
}

type fakeTapExecutor struct {
	tapResult *epb.TapResult
}

func (f fakeTapExecutor) Run(ctx context.Context, cmd *execute.Cmd) error {
	if !cmd.Tap {
		return os.ErrInvalid
	}
	anyTap, err := anypb.New(f.tapResult)
	if err != nil {
		return err
	}
	cmd.SetActionResult(&rpb.ActionResult{
		ExitCode: 0,
		ExecutionMetadata: &rpb.ExecutedActionMetadata{
			AuxiliaryMetadata: []*anypb.Any{anyTap},
		},
	}, false)
	return nil
}

func TestFileTraceExecutor_Run(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("file-access-trace available on linux only")
	}
	ctx := t.Context()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	hfs, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hfs.Close(ctx)
	if err := hfs.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	realIn := filepath.Join(dir, "real_dir/real_in.txt")
	if err := os.MkdirAll(filepath.Dir(realIn), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realIn, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	symDir := filepath.Join(dir, "sym_dir")
	if err := os.Symlink("real_dir", symDir); err != nil {
		t.Fatal(err)
	}
	symIn := filepath.Join(dir, "sym_dir/sym_in.txt")
	if err := os.Symlink("real_in.txt", filepath.Join(dir, "real_dir/sym_in.txt")); err != nil {
		t.Fatal(err)
	}
	missingIn := filepath.Join(dir, "missing_dir/missing.txt")
	outFile := filepath.Join(dir, "out/siso/out.txt")

	b := &Builder{hashFS: hfs, path: NewPath(dir, "out/siso")}
	ft := &fileTraceExecutor{
		b: b,
		executor: fakeTapExecutor{
			tapResult: &epb.TapResult{
				Reads:  []string{symIn, missingIn},
				Writes: []string{outFile},
			},
		},
	}
	cmd := &execute.Cmd{
		ID:            "test-id",
		WorkspaceRoot: dir,
		WorkDir:       path.New("out/siso"),
	}
	if err := ft.Run(ctx, cmd); err != nil {
		t.Fatalf("ft.Run(ctx, cmd) = %v; want nil", err)
	}
	wantInputs := []string{
		symDir,
		filepath.Join(dir, "real_dir/sym_in.txt"),
		realIn,
	}
	slices.Sort(wantInputs)
	if !slices.Equal(ft.inputs, wantInputs) {
		t.Errorf("ft.inputs = %q; want %q", ft.inputs, wantInputs)
	}
	wantOutputs := []string{outFile}
	if !slices.Equal(ft.outputs, wantOutputs) {
		t.Errorf("ft.outputs = %q; want %q", ft.outputs, wantOutputs)
	}
}

func setupSymlinkDirTraceTest(t *testing.T) (string, *hashfs.HashFS) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("file-access-trace available on linux only")
	}
	ctx := t.Context()
	// t.Context() is canceled before t.Cleanup functions run.
	cleanupCtx := context.WithoutCancel(ctx)
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	hfs, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := hfs.Close(cleanupCtx); err != nil {
			t.Errorf("hfs.Close = %v", err)
		}
	})
	if err := hfs.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"real_dir/in.txt",
		"real_dir/extra.txt",
		"out/siso/out.txt",
	} {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("real_dir", filepath.Join(dir, "sym_dir")); err != nil {
		t.Fatal(err)
	}
	return dir, hfs
}

func TestFileTraceExecutor_CheckTrace_SymlinkDirPure(t *testing.T) {
	dir, hfs := setupSymlinkDirTraceTest(t)
	ctx := t.Context()
	var logBuf bytes.Buffer
	b := &Builder{
		hashFS:             hfs,
		path:               NewPath(dir, "out/siso"),
		localexecLogWriter: &logBuf,
	}
	ft := &fileTraceExecutor{
		b: b,
		executor: fakeTapExecutor{
			tapResult: &epb.TapResult{
				Reads:  []string{filepath.Join(dir, "sym_dir/in.txt")},
				Writes: []string{filepath.Join(dir, "out/siso/out.txt")},
			},
		},
	}
	step := &Step{
		def: fakeStepDef{},
		cmd: &execute.Cmd{
			ID:            "test-symlink-dir-pure",
			WorkspaceRoot: dir,
			WorkDir:       path.New("out/siso"),
			Inputs:        []path.Path{"sym_dir/in.txt"},
			Outputs:       []path.Path{"out/siso/out.txt"},
			Pure:          true,
		},
	}
	if err := ft.Run(ctx, step.cmd); err != nil {
		t.Fatalf("ft.Run(ctx, step.cmd) = %v; want nil", err)
	}
	if err := ft.checkTrace(ctx, step, 0); err != nil {
		t.Fatalf("ft.checkTrace(ctx, step, 0) = %v; want nil\nlog:\n%s", err, logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "pure:true/true") {
		t.Errorf("log = %q; want it to contain pure:true/true", logBuf.String())
	}
}

func TestFileTraceExecutor_CheckTrace_SymlinkDirImpure(t *testing.T) {
	dir, hfs := setupSymlinkDirTraceTest(t)
	ctx := t.Context()
	var logBuf bytes.Buffer
	b := &Builder{
		hashFS:             hfs,
		path:               NewPath(dir, "out/siso"),
		localexecLogWriter: &logBuf,
	}
	ft := &fileTraceExecutor{
		b: b,
		executor: fakeTapExecutor{
			tapResult: &epb.TapResult{
				Reads: []string{
					filepath.Join(dir, "sym_dir/in.txt"),
					filepath.Join(dir, "sym_dir/extra.txt"),
				},
				Writes: []string{filepath.Join(dir, "out/siso/out.txt")},
			},
		},
	}
	step := &Step{
		def: fakeStepDef{},
		cmd: &execute.Cmd{
			ID:            "test-symlink-dir-impure",
			WorkspaceRoot: dir,
			WorkDir:       path.New("out/siso"),
			Inputs:        []path.Path{"sym_dir/in.txt"},
			Outputs:       []path.Path{"out/siso/out.txt"},
			Pure:          true,
		},
	}
	if err := ft.Run(ctx, step.cmd); err != nil {
		t.Fatalf("ft.Run(ctx, step.cmd) = %v; want nil", err)
	}
	if err := ft.checkTrace(ctx, step, 0); err == nil {
		t.Fatalf("ft.checkTrace(ctx, step, 0) = nil; want impure error\nlog:\n%s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "pure:true/false") || !strings.Contains(logBuf.String(), "+real_dir/extra.txt") {
		t.Errorf("log = %q; want pure:true/false and +real_dir/extra.txt", logBuf.String())
	}
}
