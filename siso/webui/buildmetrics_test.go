// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/siso/webui/invocation"
)

var _ invocation.Invocation = (*buildMetrics)(nil)

func TestBuildMetrics_CriticalPath(t *testing.T) {
	tmpDir := t.TempDir()
	metricsPath := filepath.Join(tmpDir, "siso_metrics.json")
	content := `{"build_id": "test-build-123"}
{"step_id": "step-1", "rule": "cc", "action": "clang", "outputs": ["foo.o"]}
{"step_id": "step-2", "rule": "cc", "action": "clang", "outputs": ["bar.o"], "prev": "step-1"}
{"step_id": "step-3", "rule": "cc", "action": "clang", "outputs": ["baz.o"], "prev": "step-1"}
{"step_id": "step-4", "rule": "link", "action": "ld", "outputs": ["app"], "prev": "step-3"}
`
	if err := os.WriteFile(metricsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write metrics file: %v", err)
	}

	bm, err := loadBuildMetrics(metricsPath)
	if err != nil {
		t.Fatalf("loadBuildMetrics() error = %v", err)
	}

	var inv invocation.Invocation = bm
	critPath := inv.CriticalPath()

	var gotIDs []string
	for _, step := range critPath {
		gotIDs = append(gotIDs, step.StepID)
	}

	wantIDs := []string{"step-1", "step-3", "step-4"}
	if diff := cmp.Diff(wantIDs, gotIDs); diff != "" {
		t.Errorf("CriticalPath() stepIDs mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildMetrics_CriticalPath_SingleStep(t *testing.T) {
	tmpDir := t.TempDir()
	metricsPath := filepath.Join(tmpDir, "siso_metrics.json")
	content := `{"build_id": "test-build-single"}
{"step_id": "step-1", "rule": "cc", "action": "clang", "outputs": ["foo.o"]}
`
	if err := os.WriteFile(metricsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write metrics file: %v", err)
	}

	bm, err := loadBuildMetrics(metricsPath)
	if err != nil {
		t.Fatalf("loadBuildMetrics() error = %v", err)
	}

	critPath := bm.CriticalPath()
	var gotIDs []string
	for _, step := range critPath {
		gotIDs = append(gotIDs, step.StepID)
	}

	wantIDs := []string{"step-1"}
	if diff := cmp.Diff(wantIDs, gotIDs); diff != "" {
		t.Errorf("CriticalPath() stepIDs mismatch (-want +got):\n%s", diff)
	}
}
