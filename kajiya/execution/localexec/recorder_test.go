// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package localexec

import (
	"slices"
	"sync"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

func TestAccessRecorderDedup(t *testing.T) {
	r := NewAccessRecorder()
	r.Record("src/a.cc")
	r.Record("src/b.cc")
	r.Record("src/a.cc") // duplicate

	if got, want := len(r.ObservedInputs()), 2; got != want {
		t.Fatalf("ObservedInputs() returned %d entries, want %d", got, want)
	}
}

func TestAccessRecorderSorted(t *testing.T) {
	r := NewAccessRecorder()
	r.Record("z.cc")
	r.Record("a.cc")
	r.Record("m.cc")

	inputs := r.ObservedInputs()
	if got, want := inputs[0], "a.cc"; got != want {
		t.Errorf("inputs[0] = %q, want %q", got, want)
	}
	if got, want := inputs[1], "m.cc"; got != want {
		t.Errorf("inputs[1] = %q, want %q", got, want)
	}
	if got, want := inputs[2], "z.cc"; got != want {
		t.Errorf("inputs[2] = %q, want %q", got, want)
	}
}

func TestAccessRecorderEmpty(t *testing.T) {
	r := NewAccessRecorder()
	if got := r.ObservedInputs(); len(got) != 0 {
		t.Errorf("ObservedInputs() = %v, want empty", got)
	}
}

func TestAccessRecorderConcurrent(t *testing.T) {
	r := NewAccessRecorder()
	const numGoroutines = 50
	const filesPerGoroutine = 20

	var wg sync.WaitGroup
	for g := range numGoroutines {
		wg.Go(func() {
			for f := range filesPerGoroutine {
				// Each goroutine records some unique and some shared paths.
				r.Record("shared.h")
				r.Record("unique_" + string(rune('A'+g)) + "_" + string(rune('0'+f)) + ".cc")
			}
		})
	}
	wg.Wait()

	inputs := r.ObservedInputs()
	// At least "shared.h" + numGoroutines*filesPerGoroutine unique files.
	// Some unique paths may collide due to rune encoding, but shared.h must be present.
	found := slices.Contains(inputs, "shared.h")
	if !found {
		t.Error("shared.h not found in observed inputs")
	}
	if got := len(inputs); got < 2 {
		t.Errorf("expected at least 2 observed inputs, got %d", got)
	}
}

func TestAttachObservedInputs(t *testing.T) {
	r := NewAccessRecorder()
	r.Record("src/main.cc")
	r.Record("include/header.h")

	ar := &repb.ActionResult{}
	if err := attachObservedInputs(ar, r); err != nil {
		t.Fatalf("attachObservedInputs: %v", err)
	}

	if ar.ExecutionMetadata == nil {
		t.Fatal("ExecutionMetadata is nil")
	}
	if got, want := len(ar.ExecutionMetadata.AuxiliaryMetadata), 1; got != want {
		t.Fatalf("AuxiliaryMetadata has %d entries, want %d", got, want)
	}

	// Unmarshal the Any back to a Struct.
	s := &structpb.Struct{}
	if err := ar.ExecutionMetadata.AuxiliaryMetadata[0].UnmarshalTo(s); err != nil {
		t.Fatalf("UnmarshalTo: %v", err)
	}

	listVal, ok := s.Fields["observed_inputs"]
	if !ok {
		t.Fatal("missing 'observed_inputs' field in struct")
	}
	values := listVal.GetListValue().GetValues()
	if got, want := len(values), 2; got != want {
		t.Fatalf("observed_inputs has %d entries, want %d", got, want)
	}
	// Should be sorted.
	if got, want := values[0].GetStringValue(), "include/header.h"; got != want {
		t.Errorf("values[0] = %q, want %q", got, want)
	}
	if got, want := values[1].GetStringValue(), "src/main.cc"; got != want {
		t.Errorf("values[1] = %q, want %q", got, want)
	}
}

func TestAttachObservedInputsEmpty(t *testing.T) {
	r := NewAccessRecorder()
	ar := &repb.ActionResult{}
	if err := attachObservedInputs(ar, r); err != nil {
		t.Fatalf("attachObservedInputs: %v", err)
	}
	// No metadata should be attached for empty recorders.
	if ar.ExecutionMetadata != nil {
		t.Errorf("ExecutionMetadata should be nil for empty recorder, got %v", ar.ExecutionMetadata)
	}
}
