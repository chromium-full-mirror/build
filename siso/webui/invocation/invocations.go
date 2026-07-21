// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package invocation

import (
	"iter"

	"go.chromium.org/build/siso/build"
)

// Provider serves data for one or more invocations.
// A provider could be a wrapper around an outdir e.g. out/Default, uploaded siso_metrics.json, etc.
type Provider[T Invocation] interface {
	// Get returns an invocation from this provider based on a known build ID.
	Get(id string) T
	// All returns an iterator over all invocations from this provider.
	All() iter.Seq[T]
	// Latest returns the most recent invocation known to this provider.
	Latest() T
}

// Invocation represents data for a single build invocation.
type Invocation interface {
	// ID returns the build ID.
	ID() string
	// Steps returns all build steps run in this invocation.
	Steps() []*build.StepMetric // TODO: rename to StepMetrics
}
