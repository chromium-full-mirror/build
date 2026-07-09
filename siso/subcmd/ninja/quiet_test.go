// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninja

import (
	"bytes"
	"strings"
	"testing"
	"time"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/execute"
)

func TestQuietUI_BuildActionFinished(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		ui := quietUI{
			Stdout: &stdout,
			Stderr: &stderr,
		}

		cmd := &execute.Cmd{}
		cmd.SetActionResult(&rpb.ActionResult{ExitCode: 0}, false)
		cmd.StdoutWriter().Write([]byte("command stdout\n"))
		cmd.StderrWriter().Write([]byte("command stderr\n"))
		cmd.SetOutputResult("command stdout\ncommand stderr\n")

		step := build.NewStepForTest(1, nil)
		step.SetCmdForTest(cmd)

		ui.BuildActionFinished(step)

		if got, want := stdout.String(), "command stdout\n"; got != want {
			t.Errorf("stdout = %q; want %q", got, want)
		}
		if got, want := stderr.String(), "command stderr\n"; got != want {
			t.Errorf("stderr = %q; want %q", got, want)
		}
	})

	t.Run("failure", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		ui := quietUI{
			Stdout: &stdout,
			Stderr: &stderr,
		}

		cmd := &execute.Cmd{}
		cmd.SetActionResult(&rpb.ActionResult{ExitCode: 1}, false)
		cmd.StdoutWriter().Write([]byte("command stdout\n"))
		cmd.StderrWriter().Write([]byte("error message\n"))
		failedResult := "FAILED: foo.o\n/usr/bin/gcc -c foo.c -o foo.o\ncommand stdout\nerror message\n"
		cmd.SetOutputResult(failedResult)

		step := build.NewStepForTest(1, nil)
		step.SetCmdForTest(cmd)

		ui.BuildActionFinished(step)

		if got, want := stdout.String(), ""; got != want {
			t.Errorf("stdout = %q; want %q", got, want)
		}
		if got, want := stderr.String(), failedResult; got != want {
			t.Errorf("stderr = %q; want %q", got, want)
		}
	})
}

func TestQuietSpinner(t *testing.T) {
	var stdout bytes.Buffer
	ui := quietUI{
		Stdout:          &stdout,
		heartbeatPeriod: 10 * time.Millisecond,
	}

	spinner := ui.NewSpinner()
	spinner.Start("start")
	time.Sleep(35 * time.Millisecond)
	spinner.Stop(nil)

	if !strings.Contains(stdout.String(), ".") {
		t.Errorf("expected heartbeat dots on stdout, got %q", stdout.String())
	}
}
