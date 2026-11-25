// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package localexec

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/execute"
)

func setupFakeSisoTap(t *testing.T) {
	t.Helper()
	origTapCommand := tapCommand
	origSisoTapPath := sisoTapPath
	t.Cleanup(func() {
		tapCommand = origTapCommand
		sisoTapPath = origSisoTapPath
	})

	// Ensure sisoTapOnce has fired so we can control sisoTapPath deterministically.
	_ = lookupTapCommand(t.Context())
	tapCommand = ""
	sisoTapPath = "/usr/bin/siso-tap"
}

func TestTapCmd_UntappedWhenTapFalse(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("tap is available only on linux")
	}
	ctx := t.Context()
	setupFakeSisoTap(t)

	cmd := &execute.Cmd{
		ID:   "cmd-1",
		Args: []string{"echo", "hello"},
		Tap:  false,
	}
	tcmd, postProc, err := tapCmd(ctx, cmd)
	if err != nil {
		t.Fatalf("tapCmd: %v", err)
	}
	if !slices.Equal(tcmd.Args, cmd.Args) {
		t.Errorf("tcmd.Args = %q; want %q", tcmd.Args, cmd.Args)
	}
	res := &rpb.ActionResult{ExitCode: 0}
	if err := postProc(ctx, res); err != nil {
		t.Fatalf("postProc: %v", err)
	}
	if _, ok := ExtractTapResult(res); ok {
		t.Errorf("ExtractTapResult(res) = true; want false")
	}
}

func TestTapCmd_TappedWhenCmdTapTrue(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("tap is available only on linux")
	}
	ctx := t.Context()
	setupFakeSisoTap(t)

	cmd := &execute.Cmd{
		ID:   "cmd-2",
		Args: []string{"echo", "hello"},
		Tap:  true,
	}
	tcmd, postProc, err := tapCmd(ctx, cmd)
	if err != nil {
		t.Fatalf("tapCmd: %v", err)
	}
	if len(tcmd.Args) != 6 || tcmd.Args[0] != "/usr/bin/siso-tap" || tcmd.Args[1] != "--tap_output" || tcmd.Args[3] != "--" {
		t.Fatalf("tcmd.Args = %q; want [/usr/bin/siso-tap --tap_output <file> -- echo hello]", tcmd.Args)
	}
	tapOutFile := tcmd.Args[2]
	inPath := filepath.Join(t.TempDir(), "in.txt")
	outPath := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(tapOutFile, []byte(`{"reads":["`+inPath+`"],"writes":["`+outPath+`"]}`), 0644); err != nil {
		t.Fatal(err)
	}
	res := &rpb.ActionResult{ExitCode: 0}
	if err := postProc(ctx, res); err != nil {
		t.Fatalf("postProc: %v", err)
	}
	if _, err := os.Stat(tapOutFile); !os.IsNotExist(err) {
		t.Errorf("tapOutFile %q still exists or stat err = %v; want removed", tapOutFile, err)
	}
	tapRes, ok := ExtractTapResult(res)
	if !ok {
		t.Fatalf("ExtractTapResult(res) = false; want true")
	}
	if !slices.Equal(tapRes.GetReads(), []string{inPath}) {
		t.Errorf("tapRes.Reads = %q; want [%q]", tapRes.GetReads(), inPath)
	}
	if !slices.Equal(tapRes.GetWrites(), []string{outPath}) {
		t.Errorf("tapRes.Writes = %q; want [%q]", tapRes.GetWrites(), outPath)
	}
}
