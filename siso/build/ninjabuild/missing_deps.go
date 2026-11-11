// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjabuild

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/toolsupport/ninjautil"
)

// CheckMissingDeps checks if depInputs can be found in direct/indirect inputs.
// It returns whether any missing dependency is a known issue, and an error describing
// any missing dependencies found.
func (s *StepDef) CheckMissingDeps(ctx context.Context, depInputs []path.Path) (known bool, err error) {
	deps := make(map[path.Path]bool)
	for _, dep := range depInputs {
		deps[dep] = true
	}
	visited := make(map[*ninjautil.Edge]bool)
	seen := make(map[path.Path]bool)
	// resolve deps from s.edge's inputs.
	// it doesn't check output of s.edge.
	edges := resolveEdgeDeps(s.globals, s.edge, false, deps, visited, seen)
	// avoid recursion for memory efficiency (e.g. avoid deep stack).
	// consume the queue by index: popping the front with copy() moves the
	// whole queue on every pop.
	for head := 0; head < len(edges); head++ {
		// resolve deps for edge's inputs and outputs.
		edges = append(edges, resolveEdgeDeps(s.globals, edges[head], true, deps, visited, seen)...)
		if len(deps) == 0 {
			return false, nil
		}
	}
	if len(deps) == 0 {
		return false, nil
	}
	bpath := s.globals.path
	var remaining []string
	for in := range deps {
		remaining = append(remaining, bpath.MaybeToRelative(ctx, string(in)))
	}
	sort.Strings(remaining)
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
	desc := fmt.Sprintf("deps inputs have no dependencies from %q to %q - %s", outputPath, remaining, reason)
	return known, errors.New(desc)
}

func resolveEdgeDeps(globals *globals, edge *ninjautil.Edge, checkOutputs bool, deps map[path.Path]bool, visited map[*ninjautil.Edge]bool, seen map[path.Path]bool) []*ninjautil.Edge {
	if len(deps) == 0 {
		return nil
	}
	var edges []*ninjautil.Edge
	for _, in := range edge.Inputs() {
		p := globals.targetPath(in)
		if deps[p] {
			delete(deps, p)
			if len(deps) == 0 {
				return nil
			}
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		inEdge, ok := in.InEdge()
		if !ok {
			continue
		}
		if _, ok := visited[inEdge]; ok {
			continue
		}
		visited[inEdge] = true
		edges = append(edges, inEdge)
	}
	if checkOutputs {
		for _, out := range edge.Outputs() {
			p := globals.targetPath(out)
			if deps[p] {
				delete(deps, p)
				if len(deps) == 0 {
					return nil
				}
				continue
			}
			if seen[p] {
				continue
			}
			seen[p] = true
		}
	}
	return edges
}
