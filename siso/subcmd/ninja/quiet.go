// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninja

import (
	"os"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/ui"
)

// quietUI implements ui.UI and build.StatusReporter,
// and just shows command outputs.
type quietUI struct{}

var _ build.StatusReporter = quietUI{}

func (quietUI) PlanHasTotalSteps(int)        {}
func (quietUI) BuildStepStarted(*build.Step) {}

func (quietUI) BuildStepFinished(step *build.Step) {
	os.Stderr.Write(step.Stderr())
	os.Stdout.Write(step.Stdout())
}

func (quietUI) BuildStarted()  {}
func (quietUI) BuildFinished() {}

var _ ui.UI = quietUI{}

func (quietUI) PrintLines(...string)    {}
func (quietUI) NewSpinner() ui.Spinner  { return quietSpinner{} }
func (quietUI) Infof(string, ...any)    {}
func (quietUI) Warningf(string, ...any) {}
func (quietUI) Errorf(string, ...any)   {}

type quietSpinner struct{}

func (quietSpinner) Start(string, ...any) {}
func (quietSpinner) Stop(error)           {}
func (quietSpinner) Done(string, ...any)  {}
