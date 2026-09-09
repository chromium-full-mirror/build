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
	"syscall"
	"testing"
	"time"

	seccomp "github.com/seccomp/libseccomp-golang"
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

func TestSisoTap_ConcurrentSymlinkReadlink(t *testing.T) {
	bin := buildSisoTap(t)
	tmpDir := t.TempDir()

	// Reproduce b/558156834:
	// While the target command performs concurrent symlink and readlink calls (like sbox),
	// non-fatal signals delivered to the supervisor (causing poll interruptions / POLLERR)
	// must not cause siso-tap to exit prematurely and fail readlink with ENOSYS.
	cmd := exec.Command(bin, "--", "python3", "-c", `
import os, sys, threading, time

tmpdir = sys.argv[1]
errs = []

def worker(w_id):
    for i in range(30):
        src = f"target_{w_id}_{i}"
        link = os.path.join(tmpdir, f"link_{w_id}_{i}")
        try:
            os.symlink(src, link)
            target = os.readlink(link)
            if target != src:
                errs.append(f"mismatch: {target} != {src}")
        except OSError as e:
            errs.append(f"oserror {e.errno}: {e}")
        time.sleep(0.005)

threads = [threading.Thread(target=worker, args=(i,)) for i in range(4)]
for th in threads:
    th.start()
for th in threads:
    th.join()

if errs:
    print("\n".join(errs), file=sys.stderr)
    sys.exit(1)
`, tmpDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start command: %v", err)
	}

	stopSignals := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopSignals:
				return
			case <-ticker.C:
				if cmd.Process != nil {
					_ = cmd.Process.Signal(syscall.SIGURG)
				}
			}
		}
	}()

	err := cmd.Wait()
	close(stopSignals)

	if err != nil {
		t.Fatalf("command failed: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
}

func TestSisoTap_SupervisorDoesNotExitOnPollHUPUntilDone(t *testing.T) {
	// Reproduce b/558156834:
	// If poll returns POLLHUP (or POLLERR) while no notifications are pending (!hasNotif),
	// the supervisor loop must NOT return nil prematurely until <-done has been closed.
	// Premature return would close the notify fd while the supervised process is still
	// executing, causing subsequent intercepted syscalls to fail with ENOSYS.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// Closing the write end causes poll() on r to immediately return POLLHUP.
	w.Close()

	s := &supervisor{
		fops: make(map[string]fop),
		cwds: make(map[uint32]string),
	}
	done := make(chan struct{})
	runErr := make(chan error, 1)

	go func() {
		runErr <- s.Run(seccomp.ScmpFd(r.Fd()), done)
	}()

	// The supervisor loop should keep polling and waiting for <-done,
	// and must not return prematurely because of POLLHUP.
	select {
	case err := <-runErr:
		t.Fatalf("s.Run returned prematurely before <-done: %v", err)
	case <-time.After(150 * time.Millisecond):
		// Expected: still waiting for <-done.
	}

	// Signal that the supervised process has finished.
	close(done)

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("s.Run returned error on completion: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("s.Run did not terminate after <-done was closed")
	}
}
