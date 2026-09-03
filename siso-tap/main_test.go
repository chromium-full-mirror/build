// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func buildSisoTap(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "siso-tap")
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if stdout.Len() > 0 {
		t.Logf("stdout:\n%s", stdout.String())
	}
	if stderr.Len() > 0 {
		t.Logf("stderr:\n%s", stderr.String())
	}
	if err != nil {
		t.Fatalf("failed to build siso-tap: %v", err)
	}
	return bin
}

func TestSisoTap_ExitCode(t *testing.T) {
	bin := buildSisoTap(t)

	cmd := exec.Command(bin, "--", "/bin/sh", "-c", "exit 42")
	err := cmd.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		if exitErr.ExitCode() != 42 {
			t.Fatalf("expected exit code 42, got %d", exitErr.ExitCode())
		}
	} else {
		t.Fatalf("expected *exec.ExitError, got %v", err)
	}
}

func TestSisoTap_InvalidDirFd(t *testing.T) {
	bin := buildSisoTap(t)

	// Command attempts to stat using an invalid dir fd (999).
	// Under siso-tap, this should return EBADF (Bad file descriptor),
	// and NOT hang or fail with ENOSYS (Function not implemented).
	cmd := exec.Command(bin, "--", "python3", "-c", `
import os, sys
try:
    os.stat("nonexistent", dir_fd=999)
except OSError as e:
    import errno
    if e.errno == errno.EBADF:
        sys.exit(0)
    print(f"unexpected errno: {e}", file=sys.stderr)
    sys.exit(2)
sys.exit(1)
`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\noutput: %s", err, string(out))
	}
}

func TestSisoTap_Chdir(t *testing.T) {
	bin := buildSisoTap(t)
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	tapOut := filepath.Join(tmpDir, "tap.json")
	file1 := "file1.txt"
	file2 := "file2.txt"

	cmd := exec.Command(bin, "--tap_output="+tapOut, "--", "/bin/sh", "-c", `
cd "$1"
touch "$2"
cd sub
touch "$3"
`, "script", tmpDir, file1, file2)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\noutput: %s", err, string(out))
	}

	data, err := os.ReadFile(tapOut)
	if err != nil {
		t.Fatalf("failed to read tap output: %v", err)
	}

	var tapData map[string][]string
	if err := json.Unmarshal(data, &tapData); err != nil {
		t.Fatalf("failed to parse tap output: %v", err)
	}

	expected1 := filepath.Join(tmpDir, file1)
	expected2 := filepath.Join(subDir, file2)

	writes := tapData["writes"]
	if !slices.Contains(writes, expected1) {
		t.Errorf("writes missing %q: got %v", expected1, writes)
	}
	if !slices.Contains(writes, expected2) {
		t.Errorf("writes missing %q (chdir cwd invalidation check): got %v", expected2, writes)
	}
}

func TestSisoTap_Fchdir(t *testing.T) {
	bin := buildSisoTap(t)
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	tapOut := filepath.Join(tmpDir, "tap.json")
	targetFile := "fchdir_target.txt"

	cmd := exec.Command(bin, "--tap_output="+tapOut, "--", "python3", "-c", `
import os, sys
sub_dir = sys.argv[1]
target = sys.argv[2]
fd = os.open(sub_dir, os.O_RDONLY)
os.fchdir(fd)
os.close(fd)
with open(target, "w") as f:
    f.write("ok")
`, subDir, targetFile)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\noutput: %s", err, string(out))
	}

	data, err := os.ReadFile(tapOut)
	if err != nil {
		t.Fatalf("failed to read tap output: %v", err)
	}

	var tapData map[string][]string
	if err := json.Unmarshal(data, &tapData); err != nil {
		t.Fatalf("failed to parse tap output: %v", err)
	}

	expected := filepath.Join(subDir, targetFile)
	writes := tapData["writes"]
	if !slices.Contains(writes, expected) {
		t.Errorf("writes missing %q (fchdir cwd invalidation check): got %v", expected, writes)
	}
}

func TestSisoTap_RapidSubprocesses(t *testing.T) {
	bin := buildSisoTap(t)
	tmpDir := t.TempDir()

	// Reproduce the workload that hung in b/556015638:
	// Multiple short-lived subprocesses inheriting seccomp filters.
	cmd := exec.Command(bin, "--", "/bin/sh", "-c", `
for i in $(seq 1 30); do
  echo -e -n 'test' > "$1/tmp_$i.tmp" && cmp "$1/tmp_$i.tmp" "$1/tmp_$i.tmp" && rm "$1/tmp_$i.tmp"
done
`, "script", tmpDir)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %v\noutput: %s", err, string(out))
	}
}
