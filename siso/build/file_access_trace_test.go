// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"go.chromium.org/build/siso/execute"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
)

type stubExecutor func(ctx context.Context, cmd *execute.Cmd) error

func (fn stubExecutor) Run(ctx context.Context, cmd *execute.Cmd) error {
	return fn(ctx, cmd)
}

func traceOutputArg(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-o" {
			return args[i+1]
		}
	}
	return ""
}

func TestFileTraceExecutor_RemovesTraceFile(t *testing.T) {
	ctx := t.Context()
	workDir := t.TempDir()

	t.Run("success", func(t *testing.T) {
		var traceFile string
		ft, err := newFileTraceExecutor(ctx, &Builder{}, stubExecutor(func(_ context.Context, cmd *execute.Cmd) error {
			traceFile = traceOutputArg(cmd.Args)
			if traceFile == "" {
				t.Fatalf("missing -o flag in strace args: %v", cmd.Args)
			}
			return os.WriteFile(traceFile, []byte("12345 openat(AT_FDCWD, \"in.cc\", O_RDONLY) = 3\n"), 0600)
		}))
		if err != nil {
			t.Skipf("newFileTraceExecutor unavailable: %v", err)
		}
		cmd := &execute.Cmd{
			ID:      "test-file-trace-success-" + filepath.Base(workDir),
			Args:    []string{"true"},
			WorkDir: path.New(workDir),
		}
		if err := ft.Run(ctx, cmd); err != nil {
			t.Fatalf("ft.Run=%v", err)
		}
		if _, err := os.Stat(traceFile); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("os.Stat(%q)=%v; want ErrNotExist", traceFile, err)
			_ = os.Remove(traceFile)
		}
	})

	t.Run("executor_error", func(t *testing.T) {
		var traceFile string
		wantErr := errors.New("exec failed")
		ft, err := newFileTraceExecutor(ctx, &Builder{}, stubExecutor(func(_ context.Context, cmd *execute.Cmd) error {
			traceFile = traceOutputArg(cmd.Args)
			if traceFile != "" {
				_ = os.WriteFile(traceFile, []byte("partial trace\n"), 0600)
			}
			return wantErr
		}))
		if err != nil {
			t.Skipf("newFileTraceExecutor unavailable: %v", err)
		}
		cmd := &execute.Cmd{
			ID:      "test-file-trace-err-" + filepath.Base(workDir),
			Args:    []string{"false"},
			WorkDir: path.New(workDir),
		}
		if err := ft.Run(ctx, cmd); !errors.Is(err, wantErr) {
			t.Fatalf("ft.Run=%v; want %v", err, wantErr)
		}
		if _, err := os.Stat(traceFile); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("os.Stat(%q)=%v; want ErrNotExist", traceFile, err)
			_ = os.Remove(traceFile)
		}
	})
}

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

	// Traced paths are workdir-relative (as strace reports them); declared
	// targets are workspace-relative (as cmd.AllOutputs/AllInputs yield them).
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
		"gen/extracted/a.h",
		"gen/extracted/sub/b.h",
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
	t.Run("unrelated_file_still_flagged", func(t *testing.T) {
		adds, _, _, errs := filesDiff(ctx, b, []string{"out/siso/gen/extracted"}, nil,
			append(slices.Clone(traced), "other/stray.h"), "")
		if len(errs) != 0 {
			t.Fatalf("filesDiff errs: %v", errs)
		}
		if !slices.Contains(adds, "out/siso/other/stray.h") {
			t.Errorf("adds = %v; want it to include out/siso/other/stray.h (a genuinely undeclared access)", adds)
		}
	})
}
