// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSpan_ID_Nil(t *testing.T) {
	var s *Span
	traceID, spanID := s.ID("test-project")
	if traceID != "" || spanID != "" {
		t.Errorf("s.ID() = (%q, %q); want (\"\", \"\")", traceID, spanID)
	}
}

func TestTracer_Enabled(t *testing.T) {
	ctx := t.Context()

	// 1. Nil tracer should be disabled safely
	var nilTracer *Tracer
	if nilTracer.Enabled() {
		t.Error("nilTracer.Enabled() = true; want false")
	}

	// 2. Tracer with empty filename should be disabled
	emptyTracer, err := NewTracer(ctx, "")
	if err != nil {
		t.Fatalf("NewTracer(\"\") failed: %v", err)
	}
	if emptyTracer.Enabled() {
		t.Error("emptyTracer.Enabled() = true; want false")
	}

	// 3. Tracer with valid filename should be enabled
	tempDir := t.TempDir()
	tempFile := tempDir + "/trace.json"
	fileTracer, err := NewTracer(ctx, tempFile)
	if err != nil {
		t.Fatalf("NewTracer(%q) failed: %v", tempFile, err)
	}
	defer fileTracer.Close(ctx)
	if !fileTracer.Enabled() {
		t.Error("fileTracer.Enabled() = false; want true")
	}
}

func TestTracer_CloseConcurrentWrite(t *testing.T) {
	ctx := t.Context()
	traceFile := filepath.Join(t.TempDir(), "siso_trace.json")
	te, err := NewTracer(ctx, traceFile)
	if err != nil {
		t.Fatalf("NewTracer(%q): %v", traceFile, err)
	}
	ctx = TracerContext(ctx, te)

	var wg, running sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		running.Add(1)
		wg.Go(func() {
			tctx := NewThread(ctx, "bg-worker")
			first := true
			for {
				Begin(tctx, "bg-span").End()
				if first {
					running.Done()
					first = false
				}
				select {
				case <-stop:
					return
				default:
				}
			}
		})
	}
	running.Wait() // every worker is mid-loop, so Close always overlaps writes
	te.Close(ctx)
	te.Close(ctx) // second Close must be a safe no-op
	close(stop)
	wg.Wait()

	raw, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", traceFile, err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		tail := raw[max(0, len(raw)-512):]
		t.Fatalf("trace JSON invalid after concurrent Close: %v\nlast %d bytes:\n%s", err, len(tail), tail)
	}
}
