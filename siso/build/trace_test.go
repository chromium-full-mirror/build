// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/siso/o11y/trace"
)

func TestFlowEnds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		events   []trace.Event
		src, dst flowPoint
		want     bool
	}{
		{
			name: "empty",
		},
		{
			name:   "single",
			events: []trace.Event{{T: 100, Dur: 30, Pid: 2, Tid: 7}},
			src:    flowPoint{ts: 115, pid: 2, tid: 7},
			dst:    flowPoint{ts: 100, pid: 2, tid: 7},
			want:   true,
		},
		{
			name:   "zero_width",
			events: []trace.Event{{T: 100, Pid: 2, Tid: 7}},
		},
		{
			name: "skips_zero_width",
			events: []trace.Event{
				{T: 50, Pid: 2, Tid: 7},
				{T: 100, Dur: 30, Pid: 3, Tid: 8},
				{T: 400, Pid: 4, Tid: 9},
			},
			src:  flowPoint{ts: 115, pid: 3, tid: 8},
			dst:  flowPoint{ts: 100, pid: 3, tid: 8},
			want: true,
		},
		{
			// Fractional microseconds keep a short span anchorable.
			name:   "sub_microsecond",
			events: []trace.Event{{T: 100, Dur: 0.5, Pid: 2, Tid: 7}},
			src:    flowPoint{ts: 100.25, pid: 2, tid: 7},
			dst:    flowPoint{ts: 100, pid: 2, tid: 7},
			want:   true,
		},
		{
			// 7473.334 + 26073.276/2 leaves noise digits in float64.
			name:   "rounded_midpoint",
			events: []trace.Event{{T: 7473.334, Dur: 26073.276, Pid: 2, Tid: 7}},
			src:    flowPoint{ts: 20509.972, pid: 2, tid: 7},
			dst:    flowPoint{ts: 7473.334, pid: 2, tid: 7},
			want:   true,
		},
		{
			name: "latest_end",
			events: []trace.Event{
				{T: 100, Dur: 10, Pid: 2, Tid: 7},
				{T: 120, Dur: 500, Pid: 3, Tid: 8},
				{T: 200, Dur: 10, Pid: 4, Tid: 9},
			},
			src:  flowPoint{ts: 370, pid: 3, tid: 8},
			dst:  flowPoint{ts: 100, pid: 2, tid: 7},
			want: true,
		},
		{
			name: "earliest_start",
			events: []trace.Event{
				{T: 300, Dur: 10, Pid: 2, Tid: 7},
				{T: 50, Dur: 10, Pid: 3, Tid: 8},
			},
			src:  flowPoint{ts: 305, pid: 2, tid: 7},
			dst:  flowPoint{ts: 50, pid: 3, tid: 8},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, dst, ok := flowEnds(tc.events)
			if ok != tc.want {
				t.Errorf("flowEnds(...) ok=%t; want %t", ok, tc.want)
			}
			if src != tc.src || dst != tc.dst {
				t.Errorf("flowEnds(...) = %+v, %+v; want %+v, %+v", src, dst, tc.src, tc.dst)
			}
		})
	}
}

// newTestTracer is enabled so newDepRecorder builds one, never started so
// nothing is written.
func newTestTracer(t *testing.T) *trace.Tracer {
	t.Helper()
	tracer, err := trace.NewTracer(t.Context(), filepath.Join(t.TempDir(), "siso_trace.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tracer.Close(t.Context()) })
	return tracer
}

func slice(ts, dur float64, pid, tid int64) []trace.Event {
	return []trace.Event{{T: ts, Dur: dur, Pid: pid, Tid: tid}}
}

// flowArrow is one arrow rebuilt from its event pair.
type flowArrow struct {
	src, dst flowPoint
}

// arrows rebuilds arrows from drain(), checking labels, pairing and id
// uniqueness. Sorted by target; flows are emitted in map order.
func arrows(t *testing.T, events []trace.Event) []flowArrow {
	t.Helper()
	if len(events)%2 != 0 {
		t.Fatalf("drain() returned %d events; want start and finish pairs", len(events))
	}
	seen := make(map[int64]bool)
	var got []flowArrow
	for i := 0; i < len(events); i += 2 {
		start, finish := events[i], events[i+1]
		if start.Ph != "s" || finish.Ph != "f" || finish.Bp != "e" {
			t.Errorf("arrow %d: ph %q/%q bp %q; want s, f, e", i/2, start.Ph, finish.Ph, finish.Bp)
		}
		for _, ev := range []trace.Event{start, finish} {
			if ev.Name != flowName || ev.Cat != flowCat {
				t.Errorf("arrow %d: %q/%q; want %q/%q", i/2, ev.Name, ev.Cat, flowName, flowCat)
			}
		}
		if start.ID != finish.ID {
			t.Errorf("arrow %d: start id %d, finish id %d; want one id", i/2, start.ID, finish.ID)
		}
		if seen[start.ID] {
			t.Errorf("arrow %d: flow id %d handed out twice", i/2, start.ID)
		}
		seen[start.ID] = true
		got = append(got, flowArrow{
			src: flowPoint{ts: start.T, pid: start.Pid, tid: start.Tid},
			dst: flowPoint{ts: finish.T, pid: finish.Pid, tid: finish.Tid},
		})
	}
	slices.SortFunc(got, func(a, b flowArrow) int {
		switch {
		case a.dst.ts < b.dst.ts:
			return -1
		case a.dst.ts > b.dst.ts:
			return 1
		}
		return 0
	})
	return got
}

var allowFlowUnexported = cmp.AllowUnexported(flowArrow{}, flowPoint{})

func TestDepRecorder(t *testing.T) {
	f := newDepRecorder(newTestTracer(t))
	f.add("root", "", slice(10, 40, 2, 1))
	f.add("mid", "root", slice(60, 20, 3, 4))
	f.add("leaf", "mid", slice(90, 5, 3, 5))
	f.add("orphan", "untraced", slice(95, 5, 3, 6)) // predecessor never traced
	f.add("empty", "root", nil)                     // no slices at all

	want := []flowArrow{
		{src: flowPoint{ts: 30, pid: 2, tid: 1}, dst: flowPoint{ts: 60, pid: 3, tid: 4}},
		{src: flowPoint{ts: 70, pid: 3, tid: 4}, dst: flowPoint{ts: 90, pid: 3, tid: 5}},
	}
	if diff := cmp.Diff(want, arrows(t, f.drain()), allowFlowUnexported); diff != "" {
		t.Errorf("arrows diff -want +got:\n%s", diff)
	}
}

// A step with no slice of its own must pass the arrow on, not swallow it.
func TestDepRecorderWalksThroughUnanchoredSteps(t *testing.T) {
	f := newDepRecorder(newTestTracer(t))
	f.add("root", "", slice(10, 40, 2, 1))
	f.add("hub", "root", slice(60, 0, 3, 4))
	f.add("hub2", "hub", slice(61, 0, 3, 4))
	f.add("leaf", "hub2", slice(90, 5, 3, 5))
	f.add("leaf2", "hub", slice(95, 5, 3, 6))

	// Both reach past the two sliceless steps, back to root.
	want := []flowArrow{
		{src: flowPoint{ts: 30, pid: 2, tid: 1}, dst: flowPoint{ts: 90, pid: 3, tid: 5}},
		{src: flowPoint{ts: 30, pid: 2, tid: 1}, dst: flowPoint{ts: 95, pid: 3, tid: 6}},
	}
	if diff := cmp.Diff(want, arrows(t, f.drain()), allowFlowUnexported); diff != "" {
		t.Errorf("arrows diff -want +got:\n%s", diff)
	}
}

func TestDepRecorderChainStopsAtUnanchoredRoot(t *testing.T) {
	f := newDepRecorder(newTestTracer(t))
	f.add("root", "", slice(10, 0, 2, 1))
	f.add("leaf", "root", slice(90, 5, 3, 5))
	if got := f.drain(); len(got) != 0 {
		t.Errorf("drain() = %v; want none", got)
	}
}

// A cycle must not spin forever.
func TestDepRecorderBoundsTheWalk(t *testing.T) {
	f := newDepRecorder(newTestTracer(t))
	f.add("a", "b", slice(10, 0, 2, 1))
	f.add("b", "a", slice(20, 0, 2, 1))
	f.add("leaf", "a", slice(90, 5, 3, 5))
	if got := f.drain(); len(got) != 0 {
		t.Errorf("drain() = %v; want none", got)
	}
}

// Builders share a trace file, so a repeated id pairs one build's arrow with
// another's.
func TestDepRecorderIDsAreUniquePerTracer(t *testing.T) {
	tracer := newTestTracer(t)
	seen := map[int64]bool{}
	for range 2 {
		f := newDepRecorder(tracer)
		f.add("root", "", slice(10, 40, 2, 1))
		f.add("leaf", "root", slice(60, 20, 3, 4))
		for _, ev := range f.drain() {
			if ev.ID == 0 {
				t.Errorf("flow event with no id: %+v", ev)
			}
			if ev.Ph == "s" && seen[ev.ID] {
				t.Errorf("flow id %d handed out twice", ev.ID)
			}
			if ev.Ph == "s" {
				seen[ev.ID] = true
			}
		}
	}
	if len(seen) != 2 {
		t.Errorf("got %d distinct flow ids; want 2", len(seen))
	}
}

// A build can return before its steps do.
func TestDepRecorderAddAfterDrain(t *testing.T) {
	f := newDepRecorder(newTestTracer(t))
	f.add("root", "", slice(10, 40, 2, 1))
	f.drain()
	f.add("late", "root", slice(60, 20, 3, 4))
	if got := f.drain(); len(got) != 0 {
		t.Errorf("drain() = %v; want none after the table is released", got)
	}
}

func TestDepRecorderDisabled(t *testing.T) {
	f := newDepRecorder(nil)
	if f != nil {
		t.Fatalf("newDepRecorder(nil) = %v; want nil", f)
	}
	// Must stay inert rather than panic.
	f.add("root", "", slice(10, 40, 2, 1))
	if got := f.drain(); got != nil {
		t.Errorf("drain() = %v; want nil", got)
	}
	f.record()
}
