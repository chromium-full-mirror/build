// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjabuild

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/kelindar/bitmap"

	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/toolsupport/ninjautil"
)

// CheckMissingDeps checks if depInputs can be found in direct/indirect inputs.
// It returns whether any missing dependency is a known issue, and an error describing
// any missing dependencies found.
func (s *StepDef) CheckMissingDeps(ctx context.Context, depInputs []path.Path) (known bool, err error) {
	missing := s.missingDeps(ctx, depInputs)
	if len(missing) == 0 {
		return false, nil
	}
	bpath := s.globals.path
	var outputPath string
	if len(s.edge.Outputs()) > 0 {
		out := path.New(bpath.MaybeFromRelative(ctx, s.edge.Outputs()[0].Path()))
		outputPath = toConfigPath(bpath, out)
	}
	reason := "unknown"
	v, known := s.globals.stepConfig.MissingDeps[outputPath]
	if known {
		reason = v
	}
	desc := fmt.Sprintf("deps inputs have no dependencies from %q to %q - %s", outputPath, missing, reason)
	return known, errors.New(desc)
}

// missingDeps returns the deps in depInputs that are not direct/indirect
// inputs of the step, relative to the build dir, sorted.
func (s *StepDef) missingDeps(ctx context.Context, depInputs []path.Path) []string {
	g := s.globals
	w := getInputWalker()
	defer putInputWalker(w)
	var missing []string
	for _, dep := range depInputs {
		if node, ok := g.lookupNodeByTargetPath(ctx, dep); ok {
			w.markWanted(node)
		} else {
			// A dep that is not a node in build.ninja can't be an input.
			missing = append(missing, g.path.MaybeToRelative(ctx, string(dep)))
		}
	}
	if !w.walk(s.edge) {
		for _, dep := range depInputs {
			if node, ok := g.lookupNodeByTargetPath(ctx, dep); ok && !w.hasReached(node) {
				missing = append(missing, g.path.MaybeToRelative(ctx, string(dep)))
			}
		}
	}
	slices.Sort(missing)
	return slices.Compact(missing)
}

// inputWalker walks the transitive inputs of a step until it has reached
// all the wanted nodes. It is pooled so that its bitmaps keep their
// capacity across walks.
type inputWalker struct {
	wanted    bitmap.Bitmap // ids of the wanted nodes
	remaining int           // number of wanted nodes not reached yet
	reached   bitmap.Bitmap // ids of nodes reached
	queue     []*ninjautil.Edge
}

var inputWalkerPool = sync.Pool{
	New: func() any { return &inputWalker{} },
}

func getInputWalker() *inputWalker {
	return inputWalkerPool.Get().(*inputWalker)
}

func putInputWalker(w *inputWalker) {
	// Clear zeroes a bitmap and keeps its capacity.
	w.wanted.Clear()
	w.remaining = 0
	w.reached.Clear()
	clear(w.queue)
	w.queue = w.queue[:0]
	inputWalkerPool.Put(w)
}

// markWanted adds node to the nodes that walk looks for.
func (w *inputWalker) markWanted(node *ninjautil.Node) {
	if id := uint32(node.ID()); !w.wanted.Contains(id) {
		w.wanted.Set(id)
		w.remaining++
	}
}

// walk reaches the inputs of edge, and the inputs and outputs of the edges
// that produce them, transitively and breadth first, until no wanted node
// remains. It doesn't reach the outputs of edge itself. It reports whether
// it reached all the wanted nodes.
func (w *inputWalker) walk(edge *ninjautil.Edge) bool {
	w.reachInputs(edge)
	for i := 0; w.remaining > 0 && i < len(w.queue); i++ {
		w.reachInputs(w.queue[i])
	}
	return w.remaining == 0
}

// hasReached reports whether walk reached node.
func (w *inputWalker) hasReached(node *ninjautil.Node) bool {
	return w.reached.Contains(uint32(node.ID()))
}

// reachInputs reaches the inputs of edge and enqueues the edges that
// produce them.
func (w *inputWalker) reachInputs(edge *ninjautil.Edge) {
	for _, in := range edge.Inputs() {
		if w.remaining == 0 {
			return
		}
		if !w.markReached(in) {
			continue
		}
		if producer, ok := in.InEdge(); ok {
			w.enqueue(producer)
		}
	}
}

// enqueue reaches the outputs of edge and queues edge to reach its inputs
// later. Since all its outputs are reached here, edge is queued only once.
func (w *inputWalker) enqueue(edge *ninjautil.Edge) {
	for _, out := range edge.Outputs() {
		w.markReached(out)
	}
	w.queue = append(w.queue, edge)
}

// markReached marks node reached, and reports whether it was not reached
// before.
func (w *inputWalker) markReached(node *ninjautil.Node) bool {
	id := uint32(node.ID())
	if w.reached.Contains(id) {
		return false
	}
	w.reached.Set(id)
	if w.wanted.Contains(id) {
		w.remaining--
	}
	return true
}
