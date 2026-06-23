// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.chromium.org/build/siso/execute"
)

func TestStepMetricsDone_NoExecutionMetadata(t *testing.T) {
	ctx := t.Context()
	step := &Step{
		state: &stepState{},
		cmd:   &execute.Cmd{},
	}
	var m StepMetric
	m.done(ctx, step, time.Now())
	t.Logf("m.done passed without panic")
}

func TestStepMetricsJSON_Spans(t *testing.T) {
	m := StepMetric{
		Rule: "test_rule",
		Spans: []MetricSpan{
			{
				Name:          "step:cxx",
				StartNanos:    100,
				DurationNanos: 500,
			},
		},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal(m) failed: %v", err)
	}
	got := string(b)
	want := `"spans":[{"name":"step:cxx","start_ns":100,"duration_ns":500}]`
	if !strings.Contains(got, want) {
		t.Errorf("json.Marshal(m) = %s; want to contain %s", got, want)
	}
}
