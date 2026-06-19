// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"errors"
	"runtime"
	"testing"
)

func TestLocallyNeededSet(t *testing.T) {
	var nilSet *LocallyNeededSet
	if !nilSet.Has("anything") {
		t.Errorf("nil set should return true (conservative default)")
	}

	s := NewLocallyNeededSet()
	if s.Has("not-yet-populated") {
		t.Errorf("unfrozen set should return false (keeps CAS-only outputs valid at load)")
	}

	s.add("needed/foo.o")
	s.add("needed/bar.o")

	if s.Has("not-in-map-but-unfrozen") {
		t.Errorf("unfrozen set should still return false even after adds")
	}

	s.Freeze()

	if !s.Has("needed/foo.o") {
		t.Errorf("frozen set: expected Has(foo.o)=true")
	}
	if s.Has("not-needed/baz.o") {
		t.Errorf("frozen set: expected Has(baz.o)=false")
	}
	if got, want := len(s.m), 2; got != want {
		t.Errorf("len=%d; want %d", got, want)
	}

	// OutputLocal is the predicate face; it mirrors Has.
	ctx := t.Context()
	if !s.OutputLocal(ctx, "needed/foo.o") || s.OutputLocal(ctx, "not-needed/baz.o") {
		t.Errorf("OutputLocal should mirror Has")
	}
}

// classifyStepDef embeds fakeStepDef and makes IsRemoteRule/IsPhony
// controllable, the two facts classifyLocallyNeeded reads off a step.
type classifyStepDef struct {
	fakeStepDef
	remote bool
	phony  bool
}

func (d classifyStepDef) IsRemoteRule() bool { return d.remote }
func (d classifyStepDef) IsPhony() bool      { return d.phony }

// classifyGraph embeds fakeGraph and resolves Target -> path from a map,
// the only graph method classifyLocallyNeeded uses.
type classifyGraph struct {
	fakeGraph
	paths map[Target]string
}

func (g classifyGraph) TargetPath(_ context.Context, t Target) (string, error) {
	if p, ok := g.paths[t]; ok {
		return p, nil
	}
	return "", errors.New("classifyGraph: no path for target")
}

// TestClassifyLocallyNeeded covers the per-output decision: a remote compile's
// .o (consumed by a local link) is marked, its .dwo (consumed by nothing) is
// not; a remote leaf is marked; a local step's own output is left alone.
func TestClassifyLocallyNeeded(t *testing.T) {
	ctx := t.Context()
	const (
		oObj Target = iota // remote compile .o, consumed by the local link
		oDwo               // remote compile .dwo, consumed by nothing
		oBin               // local link output (a leaf)
		oArt               // remote leaf output (requested artifact)
	)
	compileStep := NewStepForTest(1, classifyStepDef{remote: true})
	linkStep := NewStepForTest(2, classifyStepDef{remote: false})
	leafStep := NewStepForTest(3, classifyStepDef{remote: true})

	targets := make([]targetInfo, 4)
	targets[oObj] = targetInfo{
		step:  compileStep,
		edge:  &Edge{Outputs: []Target{oObj, oDwo}},
		waits: []*Step{linkStep}, // the local link consumes the .o
	}
	// oBin and oArt are the explicitly requested build targets.
	targets[oBin] = targetInfo{step: linkStep, edge: &Edge{Outputs: []Target{oBin}}, requested: true}
	targets[oArt] = targetInfo{step: leafStep, edge: &Edge{Outputs: []Target{oArt}}, requested: true}

	set := NewLocallyNeededSet()
	s := &scheduler{
		path:          &Path{WorkspaceRoot: "/w"},
		locallyNeeded: set,
		total:         3, // max Step.idnum; sizes the reaches slice
		plan: &plan{
			targets:       targets,
			targetsSorted: []Target{oObj, oBin, oArt}, // producer-first
		},
	}
	g := classifyGraph{paths: map[Target]string{
		oObj: "out/foo.o", oDwo: "out/foo.dwo", oBin: "out/chrome", oArt: "out/art",
	}}

	s.classifyLocallyNeeded(ctx, g)

	// Has is queried in the absolute makeFullpath form, so assert that form.
	if !set.Has("/w/out/foo.o") {
		t.Errorf(".o with a local consumer should be materialized")
	}
	if set.Has("/w/out/foo.dwo") {
		t.Errorf(".dwo (no consumer, sibling of the marked .o) must stay in CAS")
	}
	if !set.Has("/w/out/art") {
		t.Errorf("remote leaf (requested artifact) should be materialized")
	}
	if set.Has("/w/out/chrome") {
		t.Errorf("a local step's own output is on disk already, must not be marked")
	}
	if got, want := len(set.m), 2; got != want {
		t.Errorf("len=%d; want %d (.o and the remote leaf)", got, want)
	}
}

// TestClassifyLocallyNeeded_RequestedSibling: an explicitly requested output
// is materialized even when an edge sibling has a consumer.
func TestClassifyLocallyNeeded_RequestedSibling(t *testing.T) {
	ctx := t.Context()
	const (
		oObj Target = iota // remote .o, consumed by the local link
		oReq               // remote sibling, no consumer, but explicitly requested
		oBin               // local link output
	)
	compileStep := NewStepForTest(1, classifyStepDef{remote: true})
	linkStep := NewStepForTest(2, classifyStepDef{remote: false})

	targets := make([]targetInfo, 3)
	targets[oObj] = targetInfo{
		step:  compileStep,
		edge:  &Edge{Outputs: []Target{oObj, oReq}},
		waits: []*Step{linkStep},
	}
	targets[oReq].requested = true // user asked for the unconsumed sibling
	targets[oBin] = targetInfo{step: linkStep, edge: &Edge{Outputs: []Target{oBin}}, requested: true}

	set := NewLocallyNeededSet()
	s := &scheduler{
		path:          &Path{WorkspaceRoot: "/w"},
		locallyNeeded: set,
		total:         2,
		plan:          &plan{targets: targets, targetsSorted: []Target{oObj, oBin}},
	}
	g := classifyGraph{paths: map[Target]string{oObj: "out/foo.o", oReq: "out/foo.req", oBin: "out/chrome"}}

	s.classifyLocallyNeeded(ctx, g)

	if !set.Has("/w/out/foo.o") {
		t.Errorf(".o with a local consumer should be materialized")
	}
	if !set.Has("/w/out/foo.req") {
		t.Errorf("requested sibling must be materialized even though edge sibling .o has a consumer")
	}
}

// TestClassifyLocallyNeeded_Propagation exercises the reaches[c] branch: an
// output whose only consumer is a phony/remote intermediary is still marked
// when that intermediary reaches a local consumer (the case the strategy
// exists for, and the branch TestClassifyLocallyNeeded never hits).
func TestClassifyLocallyNeeded_Propagation(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name         string
		intermediate classifyStepDef // the sole consumer of oA
	}{
		{"through remote intermediary", classifyStepDef{remote: true}},
		{"through phony intermediary", classifyStepDef{phony: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const (
				oA Target = iota // remote-produced; consumed only by the intermediary
				oB               // produced by the intermediary; consumed by a local step
				oC               // produced by the local step (leaf)
			)
			producer := NewStepForTest(1, classifyStepDef{remote: true})
			intermediate := NewStepForTest(2, tc.intermediate)
			local := NewStepForTest(3, classifyStepDef{remote: false})

			targets := make([]targetInfo, 3)
			targets[oA] = targetInfo{step: producer, edge: &Edge{Outputs: []Target{oA}}, waits: []*Step{intermediate}}
			targets[oB] = targetInfo{step: intermediate, edge: &Edge{Outputs: []Target{oB}}, waits: []*Step{local}}
			targets[oC] = targetInfo{step: local, edge: &Edge{Outputs: []Target{oC}}}

			set := NewLocallyNeededSet()
			s := &scheduler{
				path:          &Path{WorkspaceRoot: "/w"},
				locallyNeeded: set,
				total:         3,
				plan:          &plan{targets: targets, targetsSorted: []Target{oA, oB, oC}},
			}
			g := classifyGraph{paths: map[Target]string{oA: "out/a", oB: "out/b", oC: "out/c"}}

			s.classifyLocallyNeeded(ctx, g)

			if !set.Has("/w/out/a") {
				t.Errorf("oA reaches a local consumer through the intermediary; should be marked")
			}
		})
	}
}

// TestClassifyLocallyNeeded_AbsoluteTargetPath guards the key form for an
// output whose target path is already absolute: the classifier must key it via
// hashfs.MakeFullpath (which passes absolutes through unchanged), matching how
// NeedFlush queries the predicate. A bare filepath.Join(root, abs) would
// double-prefix and silently never match, so the output would be skipped.
func TestClassifyLocallyNeeded_AbsoluteTargetPath(t *testing.T) {
	ctx := t.Context()
	const (
		oAbs Target = iota // remote-produced, target path is absolute
		oBin               // the local consumer's own output
	)
	// "/abs/..." is absolute on Unix but not on Windows (no volume), so build
	// a path the host OS treats as absolute to exercise the passthrough branch.
	absPath := "/abs/gen/foo.o"
	if runtime.GOOS == "windows" {
		absPath = `C:/abs/gen/foo.o`
	}
	producer := NewStepForTest(1, classifyStepDef{remote: true})
	local := NewStepForTest(2, classifyStepDef{remote: false})

	targets := make([]targetInfo, 2)
	targets[oAbs] = targetInfo{step: producer, edge: &Edge{Outputs: []Target{oAbs}}, waits: []*Step{local}}
	targets[oBin] = targetInfo{step: local, edge: &Edge{Outputs: []Target{oBin}}}

	set := NewLocallyNeededSet()
	s := &scheduler{
		path:          &Path{WorkspaceRoot: "/w"},
		locallyNeeded: set,
		total:         2,
		plan:          &plan{targets: targets, targetsSorted: []Target{oAbs, oBin}},
	}
	g := classifyGraph{paths: map[Target]string{oAbs: absPath, oBin: "out/bin"}}

	s.classifyLocallyNeeded(ctx, g)

	// makeFullpath passes an absolute path through unchanged (not joined under
	// the workspace root), matching how NeedFlush queries the predicate.
	if !set.Has(absPath) {
		t.Errorf("absolute target path should be keyed as-is, not joined; set=%v", set.m)
	}
}
