// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package trace

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime/metrics"
	"sync"
	"time"

	"go.chromium.org/build/siso/build/metadata"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/o11y/iometrics"
)

// Tracer records trace events in json.
type Tracer struct {
	// metadata of the build.
	metadata metadata.Metadata

	// filename of trace json file.
	fname string

	w io.Writer
	f io.Closer

	// number of traces written.
	num int

	// pass Event from Record to write.
	q chan Event
	// signals to terminate trace writer.
	quit, done chan struct{}

	// metrics samples to avoid STW from ReadMemStats.
	metricsSamples []metrics.Sample
	// resource usage record of siso.
	rusage usageRecord
	// system resource record
	sys sysRecord

	// iometrics to emit in trace json.
	ioms []*iometrics.IOMetrics
	// iostats to emit in trace json.
	iostats []iometrics.Stats
	// semaphores to emit in trace json.
	semas []Semaphore
	// last number of requests using in semaphore.
	semaReqs []int

	mu      sync.Mutex
	procs   map[string]int64
	sysPid  int64
	mainPid int64
	semaPid int64

	threads []*threadMap // string -> int
}

type Semaphore interface {
	Name() string
	NumServs() int
	NumWaits() int
	NumRequests() int
}

type threadMap struct {
	mu sync.Mutex
	m  map[string]int
}

func (tm *threadMap) get(name string) int {
	if tm == nil {
		return sisoTid
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.m == nil {
		tm.m = make(map[string]int)
	}
	tid, ok := tm.m[name]
	if ok {
		return tid
	}
	tid = len(tm.m) + sisoTid
	tm.m[name] = tid
	return tid
}

// NewTracer creates new trace json in fname.
func NewTracer(ctx context.Context, fname string) (*Tracer, error) {
	te := &Tracer{
		fname: fname,
		w:     io.Discard,
		procs: make(map[string]int64),
		metricsSamples: []metrics.Sample{
			{Name: "/memory/classes/heap/objects:bytes"},
			{Name: "/gc/heap/allocs:bytes"},
			{Name: "/memory/classes/total:bytes"},
			{Name: "/sched/pauses/total/gc:seconds"},
			{Name: "/gc/cycles/total:gc-cycles"},
		},
	}
	if te.fname != "" {
		f, err := os.Create(te.fname)
		if err != nil {
			clog.Warningf(ctx, "Failed to create %s: %v", te.fname, err)
			return nil, err
		}
		te.w = bufio.NewWriterSize(f, 256*1024)
		te.f = f
	}
	fmt.Fprintf(te.w, "{\"traceEvents\":[\n")
	te.sysPid = te.Process(ctx, "sys")
	te.mainPid = te.Process(ctx, "siso")
	return te, nil
}

var startTime = time.Now()

// StartTime returns start time of tracer.
// Event.T should be time.Since(trace.StartTime()).
func StartTime() time.Time {
	return startTime
}

func (te *Tracer) SetMetadata(metadata metadata.Metadata) {
	if te == nil {
		return
	}
	te.metadata = metadata
}

// Start starts collecting metrics from semaphores and iometrics.
func (te *Tracer) Start(ctx context.Context, semas []Semaphore, ioms []*iometrics.IOMetrics) bool {
	if te == nil {
		return false
	}
	if te.q != nil {
		return false
	}
	te.q = make(chan Event, 10000)
	te.quit = make(chan struct{})
	te.done = make(chan struct{})

	te.semas = semas
	te.semaReqs = make([]int, len(semas))
	te.ioms = ioms
	te.iostats = make([]iometrics.Stats, len(ioms))
	te.semaPid = te.Process(ctx, "siso-sema")
	for _, sema := range te.ioms {
		te.Process(ctx, "siso-io-"+sema.Name())
	}
	go te.loop(context.WithoutCancel(ctx))
	return true
}

func (te *Tracer) loop(ctx context.Context) {
	clog.Infof(ctx, "trace loop start")
	defer close(te.done)
	te.rusage.get()
	te.sys.get(ctx)
	te.rusage.start = startTime
	te.sys.start = startTime
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-te.quit:
			clog.Infof(ctx, "trace loop quit len=%d", len(te.q))
			var timeout = time.After(1 * time.Second)
		quit:
			for len(te.q) > 0 {
				select {
				case obj := <-te.q:
					te.write(ctx, obj)
				case <-timeout:
					clog.Warningf(ctx, "timed out")
					break quit
				}
			}
			clog.Infof(ctx, "trace loop quit done")
			return

		case t := <-ticker.C:
			te.sample(ctx, t)

		case obj := <-te.q:
			te.write(ctx, obj)
		}
	}
}

// Process returns pid for process name.
func (te *Tracer) Process(ctx context.Context, name string) int64 {
	if te == nil {
		return 1
	}
	te.mu.Lock()
	defer te.mu.Unlock()
	pid, ok := te.procs[name]
	if ok {
		return pid
	}
	pid = int64(len(te.procs) + 1)
	te.procs[name] = pid
	te.threads = append(te.threads, new(threadMap))
	te.write(ctx, Event{
		Name: "process_name",
		Ph:   "M",
		Pid:  pid,
		Tid:  sisoTid,
		Args: map[string]any{
			"name": name,
		},
	})
	return pid
}

// Thread returns tid for thread name in pid.
func (te *Tracer) Thread(pid int64, name string) int {
	if te == nil {
		return sisoTid
	}
	i := int(pid - 1)
	var m *threadMap
	te.mu.Lock()
	if i >= 0 && i < len(te.threads) {
		m = te.threads[i]
	}
	te.mu.Unlock()
	return m.get(name)
}

const (
	sysTid  = 1
	sisoTid = 1
)

// Event is trace event for trace json.
// see https://docs.google.com/document/d/1CvAClvFfyA5R-PhYUmn5OOQtYMH4h6I0nSsKchNAySU/preview
type Event struct {
	// The name of the event, as displayed in trace viewer.
	Name string `json:"name"`

	// The event categories.
	// This is comma separated list of categories for the event.
	// The categories can be used to hide events in the trace viewer UI.
	Cat string `json:"cat,omitempty"`

	// The event type.
	// This is a single character which changes depending on the type
	// of event being output.
	Ph string `json:"ph"`

	// The tracing clock timestamp of the event.
	// The timestamps are provided at microsecond granularity.
	T int64 `json:"ts"`

	// The process ID of the process that output this event.
	Pid int64 `json:"pid"`

	// The thread ID of the thread that output this event.
	Tid int64 `json:"tid"`

	// The tracing clock duration of complete events in microseconds.
	// Used for "ph"="X".
	Dur int64 `json:"dur,omitempty"`

	// Any arguments provided for the event.
	Args map[string]any `json:"args,omitempty"`
}

func (te *Tracer) sample(ctx context.Context, t time.Time) {
	for _, o := range te.traceMemStats(t) {
		te.write(ctx, o)
	}
	for _, o := range te.rusage.sample(te.mainPid, t) {
		te.write(ctx, o)
	}
	for _, o := range te.sys.sample(ctx, te.sysPid, t) {
		te.write(ctx, o)
	}

	for i, sema := range te.semas {
		if sema == nil {
			continue
		}
		for _, o := range te.traceSemaphore(t, sema, &te.semaReqs[i]) {
			te.write(ctx, o)
		}
	}
	for i, m := range te.ioms {
		if m == nil {
			continue
		}
		for _, o := range te.traceIOMetrics(t, te.semaPid+1+int64(i), m, &te.iostats[i]) {
			te.write(ctx, o)
		}
	}
}

func (te *Tracer) traceMemStats(t time.Time) []Event {
	var alloc, totalAlloc, sys, numGC, pauseNs uint64

	metrics.Read(te.metricsSamples)
	// See also: https://pkg.go.dev/runtime/metrics#hdr-Supported_metrics
	for _, sample := range te.metricsSamples {
		switch sample.Name {
		case "/memory/classes/heap/objects:bytes":
			if sample.Value.Kind() == metrics.KindUint64 {
				alloc = sample.Value.Uint64()
			}
		case "/gc/heap/allocs:bytes":
			if sample.Value.Kind() == metrics.KindUint64 {
				totalAlloc = sample.Value.Uint64()
			}
		case "/memory/classes/total:bytes":
			if sample.Value.Kind() == metrics.KindUint64 {
				sys = sample.Value.Uint64()
			}
		case "/gc/cycles/total:gc-cycles":
			if sample.Value.Kind() == metrics.KindUint64 {
				numGC = sample.Value.Uint64()
			}
		case "/sched/pauses/total/gc:seconds":
			// Approximate the cumulative nanoseconds in GC STW pauses.
			// runtime/metrics exports this as a Float64Histogram rather than a scalar,
			// so we estimate the total by summing (count * bucket midpoint).
			if sample.Value.Kind() == metrics.KindFloat64Histogram {
				h := sample.Value.Float64Histogram()
				var sum float64
				for j, count := range h.Counts {
					if count > 0 {
						mid := (h.Buckets[j] + h.Buckets[j+1]) / 2.0
						sum += float64(count) * mid
					}
				}
				pauseNs = uint64(sum * 1e9)
			}
		}
	}

	ret := []Event{
		{
			Name: "memstats",
			Ph:   "C",
			T:    t.Sub(startTime).Microseconds(),
			Pid:  te.mainPid,
			Tid:  sisoTid,
			Args: map[string]any{
				"alloc":       alloc,
				"total_alloc": totalAlloc,
				"sys":         sys,
				"pause":       pauseNs,
				"gc":          numGC,
			},
		},
	}
	return ret
}

func (te *Tracer) traceSemaphore(t time.Time, sema Semaphore, reqs *int) []Event {
	r := sema.NumRequests()
	rate := r - *reqs
	*reqs = r
	return []Event{
		{
			Name: sema.Name(),
			Ph:   "C",
			T:    t.Sub(startTime).Microseconds(),
			Pid:  te.semaPid,
			Tid:  sisoTid,
			Args: map[string]any{
				"queue": sema.NumWaits(),
				"serv":  sema.NumServs(),
				"rate":  rate,
			},
		},
	}
}

func (te *Tracer) traceIOMetrics(t time.Time, pid int64, m *iometrics.IOMetrics, s *iometrics.Stats) []Event {
	stats := m.Stats()

	o := Event{
		Ph:  "C",
		T:   t.Sub(startTime).Microseconds(),
		Pid: pid,
		Tid: sisoTid,
	}
	ret := make([]Event, 0, 3)
	o.Name = m.Name() + "-ops"
	o.Args = map[string]any{
		"ops/s":  stats.Ops - s.Ops,
		"errs/s": stats.OpsErrs - s.OpsErrs,
	}
	ret = append(ret, o)
	o.Name = m.Name() + "-read"
	o.Args = map[string]any{
		"ops/s":   stats.ROps - s.ROps,
		"bytes/s": stats.RBytes - s.RBytes,
		"errs/s":  stats.RErrs - s.RErrs,
	}
	ret = append(ret, o)
	o.Name = m.Name() + "-write"
	o.Args = map[string]any{
		"ops/s":   stats.WOps - s.WOps,
		"bytes/s": stats.WBytes - s.WBytes,
		"errs/s":  stats.WErrs - s.WErrs,
	}
	ret = append(ret, o)
	*s = stats
	return ret
}

func (te *Tracer) write(ctx context.Context, obj Event) {
	if te.num > 0 {
		fmt.Fprintf(te.w, ",\n ")
	}
	buf, err := json.Marshal(obj)
	if err != nil {
		clog.Warningf(ctx, "Failed to marshal %v: %v", obj, err)
		return
	}
	te.num++
	te.w.Write(buf)
}

// Record records events.
func (te *Tracer) Record(events []Event) {
	if te == nil {
		return
	}
	if len(events) == 0 {
		return
	}
	for _, obj := range events {
		te.q <- obj
	}
}

// Close closes tracer.
func (te *Tracer) Close(ctx context.Context) {
	if te == nil {
		return
	}
	if te.quit != nil {
		close(te.quit)
		<-te.done
	}
	clog.Infof(ctx, "trace finalize")
	te.writeTraceFooter(ctx)
	if b, ok := te.w.(*bufio.Writer); ok {
		if err := b.Flush(); err != nil {
			clog.Warningf(ctx, "Failed to flush: %v", err)
		}
	}
	if te.f != nil {
		if err := te.f.Close(); err != nil {
			clog.Warningf(ctx, "Failed to close trace: %v", err)
		}
	}
	clog.Infof(ctx, "trace closed")
}

func (te *Tracer) writeTraceFooter(ctx context.Context) {
	fmt.Fprintf(te.w, "\n],\n\"displayTimeUnit\":\"ms\"")
	for _, key := range te.metadata.Keys() {
		keyJSON, err := json.Marshal(key)
		if err != nil {
			clog.Warningf(ctx, "Failed to marshal metadata key %s: %v", key, err)
			continue
		}
		valJSON, err := json.Marshal(te.metadata.Get(key))
		if err != nil {
			clog.Warningf(ctx, "Failed to marshal metadata value for %s: %v", key, err)
			continue
		}
		fmt.Fprintf(te.w, ",\n%s:%s", keyJSON, valJSON)
	}
	fmt.Fprintf(te.w, "\n}")
}
