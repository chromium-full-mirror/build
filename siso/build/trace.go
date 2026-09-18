// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"go.chromium.org/build/siso/o11y/trace"
)

const (
	flowCat  = "flow"
	flowName = "dep"
)

// flowPoint is where one end of an arrow attaches, in microseconds.
type flowPoint struct {
	ts  float64
	pid int64
	tid int64
}

type depStep struct {
	prev string
	// anchored means a slice of this step has width, so an arrow can end
	// inside it at dst, the start of its first slice, or leave it from src,
	// the middle of its last. Handler-only steps such as copy emit no slice.
	src, dst flowPoint
	anchored bool
}

// depRecorder draws an arrow into each step from the input that finished
// last, the step the scheduler recorded as prev. Steps finalize in any order,
// so links are collected during the build and joined afterwards.
//
// The Builder holds a nil recorder when tracing is off; add, drain and record
// accept that. The rest run under drain's lock.
type depRecorder struct {
	tracer *trace.Tracer

	mu    sync.Mutex
	steps map[string]depStep // nil once drained
}

func newDepRecorder(tracer *trace.Tracer) *depRecorder {
	if !tracer.Enabled() {
		return nil
	}
	return &depRecorder{tracer: tracer, steps: make(map[string]depStep)}
}

// add records a step even with no slice to anchor an arrow, so the chain can
// pass through it.
func (r *depRecorder) add(id, prev string, events []trace.Event) {
	if r == nil || id == "" {
		return
	}
	src, dst, anchored := flowEnds(events)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.steps == nil {
		return // drain already ran; a step can finish after Build returned.
	}
	r.steps[id] = depStep{prev: prev, src: src, dst: dst, anchored: anchored}
}

// source walks back to the nearest step with a slice of its own; some emit
// none and still unblock others. Skipped and phony steps never get here:
// plan.completeStep already forwards prev past them.
//
// The caller holds r.mu.
func (r *depRecorder) source(prev string) (flowPoint, bool) {
	// A chain cannot be longer than the table unless it loops.
	for range len(r.steps) {
		step, ok := r.steps[prev]
		if !ok {
			return flowPoint{}, false
		}
		if step.anchored {
			return step.src, true
		}
		prev = step.prev
	}
	return flowPoint{}, false
}

// flows returns both ends of every arrow. Ids come from the tracer because
// several builders share one file.
//
// The caller holds r.mu.
func (r *depRecorder) flows() []trace.Event {
	events := make([]trace.Event, 0, 2*len(r.steps))
	for _, step := range r.steps {
		if !step.anchored || step.prev == "" {
			continue
		}
		src, ok := r.source(step.prev)
		if !ok {
			continue
		}
		id := r.tracer.NextFlowID()
		events = append(events,
			trace.Event{Name: flowName, Cat: flowCat, Ph: "s", ID: id, T: src.ts, Pid: src.pid, Tid: src.tid},
			trace.Event{Name: flowName, Cat: flowCat, Ph: "f", Bp: "e", ID: id, T: step.dst.ts, Pid: step.dst.pid, Tid: step.dst.tid})
	}
	return events
}

// drain returns everything to draw and releases the step table.
func (r *depRecorder) drain() []trace.Event {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	events := r.flows()
	r.steps = nil
	return events
}

func (r *depRecorder) record() {
	if r == nil {
		return
	}
	r.tracer.Record(r.drain())
}

// flowEnds picks a point inside the step's last slice and the start of its
// first. An arrow binds to the slice holding its timestamp, so a zero-width
// slice can hold none.
func flowEnds(events []trace.Event) (src, dst flowPoint, ok bool) {
	var first, last trace.Event
	for _, ev := range events {
		if ev.Dur <= 0 {
			continue
		}
		if !ok || ev.T < first.T {
			first = ev
		}
		if !ok || ev.T+ev.Dur > last.T+last.Dur {
			last = ev
		}
		ok = true
	}
	if !ok {
		return src, dst, false
	}
	// A slice covers [ts, ts+dur), so its end would bind to whatever follows
	// on the track. The middle is inside at any width.
	src = flowPoint{ts: nanos(last.T + last.Dur/2), pid: last.Pid, tid: last.Tid}
	dst = flowPoint{ts: first.T, pid: first.Pid, tid: first.Tid}
	return src, dst, true
}

// nanos rounds a microsecond value to whole nanoseconds, the resolution of
// every timestamp in the file. Arithmetic on two of them leaves noise digits
// otherwise.
func nanos(us float64) float64 {
	return math.Round(us*1e3) / 1e3
}

func (b *Builder) traceEvents(ctx context.Context, tc *trace.Context) []trace.Event {
	spans := tc.Spans()
	if len(spans) == 0 {
		return nil
	}

	attr := newSpanEventAttr(spans[0].Attrs)

	events := make([]trace.Event, 0, len(spans[:1]))
	for _, span := range spans[1:] {
		var obj trace.Event
		switch span.NameKind() {
		case "serv:preproc":
			obj = runPreprocSpanEvent(span, attr, b.tracePidPreproc)
		case "serv:localexec":
			obj = runLocalSpanEvent(span, attr, b.tracePidLocal)
		case "serv:remoteexec", "serv:rewrap":
			obj = runRemoteSpanEvent(span, attr, b.tracePidRemote)
		case "rbe:worker":
			worker, _ := span.Attrs["worker"].(string)
			workerID := b.tracer.Thread(ctx, b.tracePidWorker, worker)
			obj = rbeWorkerSpanEvent(span, attr, b.tracePidWorker, workerID)
		default:
			if strings.HasPrefix(span.Name, "serv:pool=") {
				obj = runLocalSpanEvent(span, attr, b.tracePidLocal)
			} else {
				continue
			}
		}
		events = append(events, obj)
	}
	b.traceDeps.add(attr.id, attr.prevID, events)
	return events
}

type spanEventAttr struct {
	id          string
	description string
	action      string
	spanName    string
	output0     string
	command     string
	backtrace   string
	prevID      string
}

func newSpanEventAttr(attr map[string]any) spanEventAttr {
	id, _ := attr["id"].(string)
	description, _ := attr["description"].(string)
	action, _ := attr["action"].(string)
	spanName, _ := attr["span_name"].(string)
	output0, _ := attr["output0"].(string)
	command, _ := attr["command"].(string)
	args := strings.Split(command, " ")
	backtraces, _ := attr[logLabelKeyBacktraces].([]string)
	if len(args) > 7 {
		args = args[:7]
		args = append(args, "...")
	}
	prevID, _ := attr["prev"].(string)
	return spanEventAttr{
		id:          id,
		description: description,
		action:      action,
		spanName:    spanName,
		output0:     output0,
		command:     strings.Join(args, " "),
		backtrace:   strings.Join(backtraces, "<"),
		prevID:      prevID,
	}
}

func runPreprocSpanEvent(span trace.SpanData, attr spanEventAttr, pid int64) trace.Event {
	return trace.Event{
		Name: attr.output0,
		Cat:  attr.spanName,
		Ph:   "X",
		T:    trace.Micros(span.Start.Sub(trace.StartTime())),
		Pid:  pid,
		Tid:  int64(span.Tid),
		Dur:  trace.Micros(span.Duration()),
		Args: map[string]any{
			"id":          attr.id,
			"description": attr.description,
			"action":      attr.action,
			"command":     attr.command,
			"backtrace":   attr.backtrace,
			"prev_id":     attr.prevID,
		},
	}
}

func runLocalSpanEvent(span trace.SpanData, attr spanEventAttr, pid int64) trace.Event {
	return trace.Event{
		Name: attr.output0,
		Cat:  attr.spanName,
		Ph:   "X",
		T:    trace.Micros(span.Start.Sub(trace.StartTime())),
		Pid:  pid,
		Tid:  int64(span.Tid),
		Dur:  trace.Micros(span.Duration()),
		Args: map[string]any{
			"id":          attr.id,
			"description": attr.description,
			"action":      attr.action,
			"command":     attr.command,
			"backtrace":   attr.backtrace,
			"prev_id":     attr.prevID,
		},
	}
}

func runRemoteSpanEvent(span trace.SpanData, attr spanEventAttr, pid int64) trace.Event {
	return trace.Event{
		Name: attr.output0,
		Cat:  attr.spanName,
		Ph:   "X",
		T:    trace.Micros(span.Start.Sub(trace.StartTime())),
		Pid:  pid,
		Tid:  int64(span.Tid),
		Dur:  trace.Micros(span.Duration()),
		Args: map[string]any{
			"id":          attr.id,
			"description": attr.description,
			"action":      attr.action,
			"command":     attr.command,
			"backtrace":   attr.backtrace,
			"prev_id":     attr.prevID,
		},
	}
}

func rbeWorkerSpanEvent(span trace.SpanData, attr spanEventAttr, pid int64, workerID int) trace.Event {
	return trace.Event{
		Name: attr.output0,
		Cat:  attr.spanName,
		Ph:   "X",
		T:    trace.Micros(span.Start.Sub(trace.StartTime())),
		Pid:  pid,
		Tid:  int64(workerID),
		Dur:  trace.Micros(span.Duration()),
		Args: map[string]any{
			"id":          attr.id,
			"description": attr.description,
			"action":      attr.action,
			"command":     attr.command,
			"backtrace":   attr.backtrace,
			"prev_id":     attr.prevID,
			"worker":      span.Attrs["worker"].(string),
		},
	}
}

type traceStats struct {
	mu sync.Mutex
	s  map[string]*TraceStat
}

func newTraceStats() *traceStats {
	return &traceStats{
		s: make(map[string]*TraceStat),
	}
}

// TraceStat is trace statistics.
type TraceStat struct {
	// Name is trace name.
	Name string

	// N is count of the trace.
	N int

	// NErr is error count of the trace.
	NErr int

	// Total is total duration of the trace.
	Total time.Duration

	// Max is max duration of the trace.
	Max time.Duration

	// Buckets are buckets of trace durations.
	//
	//  0: [0,10ms)
	//  1: [10ms, 100ms)
	//  2: [100ms, 1s)
	//  3: [1s, 10s)
	//  4: [10s, 1m)
	//  5: [1m, 10m)
	//  6: >=10m
	Buckets [7]int
}

func bucketIndex(dur time.Duration) int {
	switch {
	case dur < 10*time.Millisecond:
		return 0
	case dur < 100*time.Millisecond:
		return 1
	case dur < 1*time.Second:
		return 2
	case dur < 10*time.Second:
		return 3
	case dur < 1*time.Minute:
		return 4
	case dur < 10*time.Minute:
		return 5
	default:
		return 6
	}
}

func (t *TraceStat) update(dur time.Duration, isErr bool) {
	t.N++
	if isErr {
		t.NErr++
	}
	t.Total += dur
	if t.Max < dur {
		t.Max = dur
	}
	t.Buckets[bucketIndex(dur)]++
}

// Avg returns average duration of the trace.
func (t *TraceStat) Avg() time.Duration {
	return t.Total / time.Duration(int64(t.N))
}

func (s *traceStats) update(tc *trace.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, span := range tc.Spans() {
		ts, ok := s.s[span.Name]
		if !ok {
			ts = &TraceStat{Name: span.Name}
			s.s[span.Name] = ts
		}
		ts.update(span.Duration(), span.Status != nil)
	}
}

func (s *traceStats) get() []*TraceStat {
	var ret []*TraceStat
	s.mu.Lock()
	for _, ts := range s.s {
		ret = append(ret, ts)
	}
	s.mu.Unlock()
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Avg() > ret[j].Avg()
	})
	return ret
}
