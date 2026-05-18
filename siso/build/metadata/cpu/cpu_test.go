// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package cpu_test

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"go.chromium.org/build/siso/build/metadata/cpu"
)

func TestLogicalCores(t *testing.T) {
	// Get expectation from Python's os.cpu_count().
	cmd := exec.Command("python3", "-c", "import os; print(os.cpu_count())")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to run command: %v", err)
	}
	want, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("failed to convert %s to int", out)
	}

	n := cpu.LogicalCores()
	if n != want {
		t.Errorf("LogicalCores()=%d, want %d", n, want)
	}
}
