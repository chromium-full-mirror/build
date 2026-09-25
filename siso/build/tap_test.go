// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/golang/glog"
	"google.golang.org/protobuf/types/known/anypb"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/execute"
	pb "go.chromium.org/build/siso/execute/proto"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
)

func init() {
	glog.Infof("create log file before setting TMPDIR")
}

func setTapResult(cmd *execute.Cmd, reads, writes, deletes []string) error {
	tapData := &pb.TapResult{
		Reads:   reads,
		Writes:  writes,
		Deletes: deletes,
	}
	any, err := anypb.New(tapData)
	if err != nil {
		return err
	}
	res := &rpb.ActionResult{
		ExecutionMetadata: &rpb.ExecutedActionMetadata{
			AuxiliaryMetadata: []*anypb.Any{any},
		},
	}
	cmd.SetActionResult(res, false)
	return nil
}

func setupDirForTapTest(t *testing.T) (string, string) {
	tmpdir := t.TempDir()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmpdir)
	t.Setenv("TMP", tmpdir)
	if path.New(dir).HasPrefix(path.New(tmpdir)) {
		t.Fatalf("dir=%q under TMPDIR=%q", dir, tmpdir)
	}
	wsDir := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(wsDir, 0755); err != nil {
		t.Fatal(err)
	}
	customTmpDir := filepath.Join(dir, "workspace/out/soong/.temp")
	if err := os.MkdirAll(customTmpDir, 0755); err != nil {
		t.Fatal(err)
	}
	return dir, tmpdir
}

func TestTapCanonicalizeCmd_NoTapData(t *testing.T) {
	ctx := t.Context()
	dir, _ := setupDirForTapTest(t)
	wsDir := filepath.Join(dir, "workspace")

	hfs, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hfs.Close(ctx)
	err = hfs.WaitReady(ctx)
	if err != nil {
		t.Fatal(err)
	}

	b := &Builder{
		hashFS: hfs,
		path:   NewPath(wsDir, "out/siso"),
	}

	cmd := &execute.Cmd{
		WorkspaceRoot: wsDir,
	}

	err = b.tapCanonicalizeCmd(ctx, cmd)
	if err == nil {
		t.Errorf("tapCanonicalizeCmd(cmd) = nil; want error")
	}
}

func TestTapCanonicalizeCmd(t *testing.T) {
	ctx := t.Context()
	dir, tmpDir := setupDirForTapTest(t)
	wsDir := filepath.Join(dir, "workspace")

	hfs, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hfs.Close(ctx)
	if err := hfs.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}

	setupFile := func(root, rel string) string {
		t.Helper()
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		return abs
	}
	setupDir := func(root, rel string) string {
		t.Helper()
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(abs, 0755); err != nil {
			t.Fatal(err)
		}
		return abs
	}

	b := &Builder{
		hashFS: hfs,
		path:   NewPath(wsDir, "out/siso"),
	}

	existingInput := setupFile(wsDir, "input_existing.h")
	newInput := setupFile(wsDir, "input_new.h")
	nonExistentInput := filepath.Join(wsDir, "input_nonexistent.h")
	outsideInput := setupFile(tmpDir, "outside_input.h")

	existingOutput := setupFile(wsDir, "out/siso/output_existing.o")
	newOutput := setupFile(wsDir, "out/siso/output_new.o")
	dirOutput := setupDir(wsDir, "out/siso/output_dir")

	toDeleteFile := setupFile(wsDir, "to_delete.txt")

	// Cache toDeleteFile in hashFS first
	_, err = hfs.Stat(ctx, wsDir, path.New("to_delete.txt"))
	if err != nil {
		t.Fatalf("hfs.Stat to_delete.txt failed: %v", err)
	}

	// Remove toDeleteFile from disk to simulate action deletion
	if err := os.Remove(toDeleteFile); err != nil {
		t.Fatal(err)
	}

	cmd := &execute.Cmd{
		WorkspaceRoot: wsDir,
		Inputs:        []path.Path{path.New("input_existing.h")},
		Outputs:       []path.Path{path.New("out/siso/output_existing.o")},
	}

	err = setTapResult(cmd,
		[]string{existingInput, newInput, nonExistentInput, outsideInput},
		[]string{existingOutput, newOutput, dirOutput},
		[]string{toDeleteFile},
	)
	if err != nil {
		t.Fatal(err)
	}

	err = b.tapCanonicalizeCmd(ctx, cmd)
	if err != nil {
		t.Fatalf("tapCanonicalizeCmd returned error: %v", err)
	}

	if !cmd.Pure {
		t.Errorf("cmd.Pure = false; want true")
	}

	wantInputs := []path.Path{path.New("input_existing.h"), path.New("input_new.h")}
	if !slices.Equal(cmd.Inputs, wantInputs) {
		t.Errorf("cmd.Inputs = %v; want %v", cmd.Inputs, wantInputs)
	}

	wantOutputs := []path.Path{path.New("out/siso/output_existing.o"), path.New("out/siso/output_new.o")}
	if !slices.Equal(cmd.Outputs, wantOutputs) {
		t.Errorf("cmd.Outputs = %v; want %v", cmd.Outputs, wantOutputs)
	}

	wantOutputDirs := []path.Path{path.New("out/siso/output_dir")}
	if !slices.Equal(cmd.OutputDirs, wantOutputDirs) {
		t.Errorf("cmd.OutputDirs = %v; want %v", cmd.OutputDirs, wantOutputDirs)
	}

	// Verify to_delete.txt is forgotten from hashFS and returns ErrNotExist
	_, err = hfs.Stat(ctx, wsDir, path.New("to_delete.txt"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("hfs.Stat(to_delete.txt) = %v; want %v after forget", err, fs.ErrNotExist)
	}
}

func TestTapCanonicalizeCmd_IgnoreOSTMPDIR(t *testing.T) {
	ctx := t.Context()
	dir, _ := setupDirForTapTest(t)
	wsDir := filepath.Join(dir, "workspace")
	customTmpDir := filepath.Join(dir, "workspace/out/soong/.temp")
	t.Setenv("TMPDIR", customTmpDir)
	t.Setenv("TMP", customTmpDir)

	hfs, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hfs.Close(ctx)
	err = hfs.WaitReady(ctx)
	if err != nil {
		t.Fatal(err)
	}

	setupFile := func(abs string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	wsInput := filepath.Join(wsDir, "input.h")
	setupFile(wsInput)

	tmpRead := filepath.Join(os.TempDir(), "tmp_read.h")
	setupFile(tmpRead)
	defer os.Remove(tmpRead)

	customTmpRead := filepath.Join(customTmpDir, "custom_tmp_read.h")
	setupFile(customTmpRead)

	customTmpWrite := filepath.Join(customTmpDir, "custom_tmp_write.o")
	setupFile(customTmpWrite)

	b := &Builder{
		hashFS: hfs,
		path:   NewPath(wsDir, "out/siso"),
	}

	cmd := &execute.Cmd{
		WorkspaceRoot: wsDir,
	}

	err = setTapResult(cmd,
		[]string{wsInput, tmpRead, customTmpRead},
		[]string{customTmpWrite},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	err = b.tapCanonicalizeCmd(ctx, cmd)
	if err != nil {
		t.Fatalf("tapCanonicalizeCmd returned error: %v", err)
	}

	wantInputs := []path.Path{path.New("input.h")}
	if !slices.Equal(cmd.Inputs, wantInputs) {
		t.Errorf("cmd.Inputs = %v; want %v (tmp reads should be ignored)", cmd.Inputs, wantInputs)
	}

	if len(cmd.Outputs) != 0 {
		t.Errorf("cmd.Outputs = %v; want empty (tmp writes should be ignored)", cmd.Outputs)
	}
}

func TestTapCanonicalizeCmd_IgnoreCmdEnvTMPDIR(t *testing.T) {
	ctx := t.Context()
	dir, _ := setupDirForTapTest(t)
	wsDir := filepath.Join(dir, "workspace")
	customTmpDir := filepath.Join(dir, "workspace/out/soong/.temp")

	hfs, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hfs.Close(ctx)
	err = hfs.WaitReady(ctx)
	if err != nil {
		t.Fatal(err)
	}

	setupFile := func(abs string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	wsInput := filepath.Join(wsDir, "input.h")
	setupFile(wsInput)

	tmpRead := filepath.Join(os.TempDir(), "tmp_read.h")
	setupFile(tmpRead)
	defer os.Remove(tmpRead)

	customTmpRead := filepath.Join(customTmpDir, "custom_tmp_read.h")
	setupFile(customTmpRead)

	customTmpWrite := filepath.Join(customTmpDir, "custom_tmp_write.o")
	setupFile(customTmpWrite)

	b := &Builder{
		hashFS: hfs,
		path:   NewPath(wsDir, "out/siso"),
	}

	cmd := &execute.Cmd{
		WorkspaceRoot: wsDir,
		Env:           []string{"TMPDIR=" + customTmpDir},
	}

	err = setTapResult(cmd,
		[]string{wsInput, tmpRead, customTmpRead},
		[]string{customTmpWrite},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	err = b.tapCanonicalizeCmd(ctx, cmd)
	if err != nil {
		t.Fatalf("tapCanonicalizeCmd returned error: %v", err)
	}

	wantInputs := []path.Path{path.New("input.h")}
	if !slices.Equal(cmd.Inputs, wantInputs) {
		t.Errorf("cmd.Inputs = %v; want %v (tmp reads should be ignored)", cmd.Inputs, wantInputs)
	}

	if len(cmd.Outputs) != 0 {
		t.Errorf("cmd.Outputs = %v; want empty (tmp writes should be ignored)", cmd.Outputs)
	}
}

func TestTapCanonicalizeCmd_Symlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no symlink test on windows")
		return
	}

	ctx := t.Context()
	dir, tmpDir := setupDirForTapTest(t)
	wsDir := filepath.Join(dir, "workspace")

	hfs, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hfs.Close(ctx)
	if err := hfs.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}

	setupFile := func(rel string) {
		t.Helper()
		abs := filepath.Join(wsDir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	setupSymlink := func(rel, target string) string {
		t.Helper()
		abs := filepath.Join(wsDir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, abs); err != nil {
			t.Fatal(err)
		}
		return abs
	}

	setupFile("target.h")
	symlinkInput := setupSymlink("symlink.h", "target.h")
	setupFile("dir/target2.h")
	subSymlinkInput := setupSymlink("symlink_sub.h", "dir/target2.h")
	outsideFile := filepath.Join(tmpDir, "outside.h")
	if err := os.WriteFile(outsideFile, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	outsideSymlinkInput := setupSymlink("symlink_outside.h", outsideFile)

	b := &Builder{
		hashFS: hfs,
		path:   NewPath(wsDir, "out/siso"),
	}

	cmd := &execute.Cmd{
		WorkspaceRoot: wsDir,
	}

	err = setTapResult(cmd,
		[]string{symlinkInput, subSymlinkInput, outsideSymlinkInput},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	err = b.tapCanonicalizeCmd(ctx, cmd)
	if err != nil {
		t.Fatalf("tapCanonicalizeCmd returned error: %v", err)
	}

	wantInputs := []path.Path{
		path.New("symlink.h"),
		path.New("target.h"),
		path.New("symlink_sub.h"),
		path.New("dir/target2.h"),
		path.New("symlink_outside.h"),
	}
	if !slices.Equal(cmd.Inputs, wantInputs) {
		t.Errorf("cmd.Inputs = %v; want %v", cmd.Inputs, wantInputs)
	}
}

func TestTapCanonicalizeCmd_TapDetectedZero(t *testing.T) {
	dir, tmpDir := setupDirForTapTest(t)
	wsDir := filepath.Join(dir, "workspace")
	customTmpDir := filepath.Join(dir, "workspace/out/soong/.temp")

	setupFile := func(root, rel string) string {
		t.Helper()
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		return abs
	}

	outsideInput := setupFile(tmpDir, "outside_input.h")
	outsideOutput := setupFile(tmpDir, "outside_output.o")
	customTmpInput := setupFile(customTmpDir, "custom_tmp_read.h")
	customTmpOutput := setupFile(customTmpDir, "custom_tmp_write.o")

	for _, tc := range []struct {
		name    string
		env     []string
		reads   []string
		writes  []string
		deletes []string
	}{
		{
			name: "empty",
		},
		{
			name:   "outside_workspace",
			reads:  []string{outsideInput},
			writes: []string{outsideOutput},
		},
		{
			name:   "nonexistent_files",
			reads:  []string{filepath.Join(wsDir, "nonexistent.h")},
			writes: []string{filepath.Join(wsDir, "out/siso/nonexistent.o")},
		},
		{
			name:   "only_tmpdir",
			env:    []string{"TMPDIR=" + customTmpDir},
			reads:  []string{customTmpInput},
			writes: []string{customTmpOutput},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			hfs, err := hashfs.New(ctx, hashfs.Option{})
			if err != nil {
				t.Fatal(err)
			}
			defer hfs.Close(ctx)
			if err := hfs.WaitReady(ctx); err != nil {
				t.Fatal(err)
			}

			b := &Builder{
				hashFS: hfs,
				path:   NewPath(wsDir, "out/siso"),
			}

			cmd := &execute.Cmd{
				WorkspaceRoot: wsDir,
				Env:           tc.env,
			}

			err = setTapResult(cmd, tc.reads, tc.writes, tc.deletes)
			if err != nil {
				t.Fatal(err)
			}

			err = b.tapCanonicalizeCmd(ctx, cmd)
			if err == nil {
				t.Fatalf("tapCanonicalizeCmd returned nil error; want error")
			}
			t.Logf("tapCanonicalizeCmd: %v", err)
			if cmd.Pure {
				t.Errorf("cmd.Pure = true; want false")
			}
		})
	}
}

func TestTapCanonicalizeCmd_OutputDir(t *testing.T) {
	ctx := t.Context()
	dir, _ := setupDirForTapTest(t)
	wsDir := filepath.Join(dir, "workspace")

	hfs, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hfs.Close(ctx)
	if err := hfs.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}

	setupFile := func(root, rel string) string {
		t.Helper()
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		return abs
	}

	b := &Builder{
		hashFS: hfs,
		path:   NewPath(wsDir, "out/siso"),
	}

	existingInput := setupFile(wsDir, "input.h")
	existingOutput := setupFile(wsDir, "out/siso/output.o")
	fileInDirOutput := setupFile(wsDir, "out/siso/output_dir/nested/file.txt")
	fileInAuxDirOutput := setupFile(wsDir, "out/siso/aux_dir/log.txt")
	standaloneOutput := setupFile(wsDir, "out/siso/standalone.txt")

	cmd := &execute.Cmd{
		WorkspaceRoot:          wsDir,
		Inputs:                 []path.Path{path.New("input.h")},
		Outputs:                []path.Path{path.New("out/siso/output.o")},
		OutputDirs:             []path.Path{path.New("out/siso/output_dir")},
		AuxiliaryLogOutputDirs: []path.Path{path.New("out/siso/aux_dir")},
	}

	err = setTapResult(cmd,
		[]string{existingInput},
		[]string{existingOutput, fileInDirOutput, fileInAuxDirOutput, standaloneOutput},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	err = b.tapCanonicalizeCmd(ctx, cmd)
	if err != nil {
		t.Fatalf("tapCanonicalizeCmd returned error: %v", err)
	}

	if !cmd.Pure {
		t.Errorf("cmd.Pure = false; want true")
	}

	wantOutputs := []path.Path{path.New("out/siso/output.o"), path.New("out/siso/standalone.txt")}
	if !slices.Equal(cmd.Outputs, wantOutputs) {
		t.Errorf("cmd.Outputs = %v; want %v", cmd.Outputs, wantOutputs)
	}
}
