// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"go.chromium.org/build/siso/execute"
	"go.chromium.org/build/siso/ui"
)

// newProgressTestSteps builds three steps in started state with
// staggered startTimes, suitable for driving the update goroutine
// under synctest.
func newProgressTestSteps() (s0, s1, s2 *Step) {
	now := time.Now()
	mkStep := func(desc string, start time.Time) *Step {
		s := &Step{
			cmd:       &execute.Cmd{Desc: desc},
			state:     &stepState{},
			startTime: start,
		}
		s.setPhase(stepStart)
		return s
	}
	s0 = mkStep("head", now.Add(-3*time.Second))
	s1 = mkStep("middle", now.Add(-2*time.Second))
	s2 = mkStep("tail", now.Add(-1*time.Second))
	return
}

// TestProgress_DoneMiddleStepRemovedFromActives checks that update()
// removes a done step from the middle of p.actives, not only from the
// head.
func TestProgress_DoneMiddleStepRemovedFromActives(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		currentUI := ui.Default
		defer func() { ui.Default = currentUI }()
		ui.Default = &ui.LogUI{}

		var p progress
		b := &Builder{
			statusReporter: noopStatusReporter{},
			plan:           &plan{},
			stats:          &stats{},
		}

		s0, s1, s2 := newProgressTestSteps()

		p.start(t.Context(), b)
		t.Cleanup(p.stop)

		p.step(b, s0, progressPrefixStart+s0.cmd.Desc)
		p.step(b, s1, progressPrefixStart+s1.cmd.Desc)
		p.step(b, s2, progressPrefixStart+s2.cmd.Desc)
		s1.setPhase(stepDone)

		// Advance virtual time past one tick (ticker is 100ms).
		// synctest intercepts Sleep and advances fake time only when
		// all bubble goroutines are durably blocked, so by the time
		// Sleep returns the tick has fired and update() is parked on
		// the next ticker receive.
		time.Sleep(150 * time.Millisecond)
		synctest.Wait()

		p.mu.Lock()
		got := len(p.actives)
		p.mu.Unlock()

		if want := 2; got != want {
			t.Errorf("len(p.actives)=%d after middle step finished; want %d", got, want)
		}
	})
}

// TestProgress_WeightedDurationExcludesDoneStep checks that update()
// divides the tick duration across live steps only.
func TestProgress_WeightedDurationExcludesDoneStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		currentUI := ui.Default
		defer func() { ui.Default = currentUI }()
		ui.Default = &ui.LogUI{}

		var p progress
		b := &Builder{
			statusReporter: noopStatusReporter{},
			plan:           &plan{},
			stats:          &stats{},
		}

		s0, s1, s2 := newProgressTestSteps()

		p.start(t.Context(), b)
		t.Cleanup(p.stop)

		p.step(b, s0, progressPrefixStart+s0.cmd.Desc)
		p.step(b, s1, progressPrefixStart+s1.cmd.Desc)
		p.step(b, s2, progressPrefixStart+s2.cmd.Desc)
		s1.setPhase(stepDone)

		// Let the first tick run the done sweep so only s0 and s2
		// remain in actives when the next tick accounts weight.
		time.Sleep(150 * time.Millisecond)
		synctest.Wait()

		w0Before := s0.getWeightedDuration()
		w2Before := s2.getWeightedDuration()

		// Advance one more full tick. With 2 live steps, each should
		// receive d/2 = 50ms of weight.
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()

		w0 := s0.getWeightedDuration() - w0Before
		w2 := s2.getWeightedDuration() - w2Before

		// Under synctest virtual time, Sleep is exact and the ticker
		// d is deterministically 100ms. Allow 1ms slack for the
		// time.Since measurement inside update() crossing a tick.
		want := 50 * time.Millisecond
		slack := 1 * time.Millisecond
		if w0 < want-slack || w0 > want+slack {
			t.Errorf("s0 weight gained=%v in one tick; want %v (+-%v)", w0, want, slack)
		}
		if w2 < want-slack || w2 > want+slack {
			t.Errorf("s2 weight gained=%v in one tick; want %v (+-%v)", w2, want, slack)
		}
		if w1 := s1.getWeightedDuration(); w1 != 0 {
			t.Errorf("s1 (done) weight=%v; want 0", w1)
		}
	})
}

func TestProgress_NotIsTerminal(t *testing.T) {
	currentUI := ui.Default
	defer func() { ui.Default = currentUI }()
	ui.Default = &ui.LogUI{}
	var p progress
	b := &Builder{
		statusReporter: noopStatusReporter{},
		plan:           &plan{},
		stats:          &stats{},
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p.start(ctx, b)

	step := &Step{
		cmd: &execute.Cmd{
			Desc: "ACTION sample",
		},
		state: &stepState{},
	}
	step.setPhase(stepStart)
	p.step(b, step, progressPrefixStart)
	started := time.Now()
	var count int64
	for count == 0 && time.Since(started) < 1*time.Second {
		time.Sleep(200 * time.Millisecond)
		count = p.count.Load()
		t.Logf("count=%d", count)
	}
	if count == 0 {
		t.Errorf("progress count=%d; want >0", count)
	}
	step.setPhase(stepDone)
	p.step(b, step, progressPrefixFinish)
	p.stop()

	if w := step.getWeightedDuration(); w == 0 {
		t.Errorf("weighted_duration=0; want non-zero")
	}
}
