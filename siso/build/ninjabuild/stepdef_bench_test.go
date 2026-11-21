// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjabuild

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/toolsupport/ninjautil"
)

// newTestGraph loads manifest as build.ninja in dir (out/Default as build
// dir) and returns a Graph with minimal globals to create StepDefs.
func newTestGraph(tb testing.TB, dir, manifest string) (*Graph, *ninjautil.State) {
	tb.Helper()
	ctx := tb.Context()
	fname := filepath.Join(dir, "build.ninja")
	if err := os.WriteFile(fname, []byte(manifest), 0644); err != nil {
		tb.Fatal(err)
	}
	state := ninjautil.NewState()
	if err := ninjautil.NewManifestParser(state).Load(ctx, fname); err != nil {
		tb.Fatalf("load: %v\n%s", err, manifest)
	}
	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := hashFS.Close(context.WithoutCancel(ctx)); err != nil {
			tb.Errorf("hashFS.Close=%v", err)
		}
	})
	graph := &Graph{
		visited: make(map[*ninjautil.Edge]*build.Edge),
		globals: &globals{
			nstate:      state,
			path:        build.NewPath(dir, "out/Default"),
			hashFS:      hashFS,
			stepConfig:  &StepConfig{},
			targetPaths: make([]path.Path, state.NumNodes()),
			edgeRules:   make([]edgeRuleHolder, state.NumNodes()),
		},
	}
	if err := graph.globals.stepConfig.Init(ctx); err != nil {
		tb.Fatal(err)
	}
	return graph, state
}

// newTestStepDef returns the StepDef of the edge that builds target.
func newTestStepDef(tb testing.TB, graph *Graph, state *ninjautil.State, target string) *StepDef {
	tb.Helper()
	ctx := tb.Context()
	node, ok := state.LookupNodeByPath(target)
	if !ok {
		tb.Fatalf("%s not found", target)
	}
	edge, ok := node.InEdge()
	if !ok {
		tb.Fatalf("no edge for %s", target)
	}
	s := graph.newStepDef(ctx, edge, nil)
	s.EnsureRule(ctx)
	return s
}

// benchManifest has a step "t" with 2000 direct inputs and 50 phony
// inputs, each of which expands to 40 inputs through a nested phony, and
// shares half of them with the next one.
func benchManifest() string {
	var b strings.Builder
	b.WriteString("rule r\n  command = x\n")
	for i := range 50 {
		var ins []string
		for j := range 20 {
			ins = append(ins, fmt.Sprintf("../../src/p%d/f%d.h", i, j))
			ins = append(ins, fmt.Sprintf("../../src/p%d/f%d.h", i+1, j))
		}
		fmt.Fprintf(&b, "build inner%d: phony %s\n", i, strings.Join(ins, " "))
		fmt.Fprintf(&b, "build ph%d: phony inner%d\n", i, i)
	}
	var ins []string
	for i := range 2000 {
		ins = append(ins, fmt.Sprintf("../../src/direct/f%d.cc", i))
	}
	for i := range 50 {
		ins = append(ins, fmt.Sprintf("ph%d", i))
	}
	fmt.Fprintf(&b, "build t: r %s\n", strings.Join(ins, " "))
	return b.String()
}

func BenchmarkDepsBaseInputs(b *testing.B) {
	ctx := b.Context()
	graph, state := newTestGraph(b, b.TempDir(), benchManifest())
	s := newTestStepDef(b, graph, state, "t")
	// the phony targets themselves are inputs too.
	if n := len(s.DepsBaseInputs(ctx, nil, false)); n != 2000+50+51*20 {
		b.Fatalf("DepsBaseInputs returned %d inputs; want %d", n, 2000+50+51*20)
	}
	b.ReportAllocs()
	for b.Loop() {
		s.DepsBaseInputs(ctx, nil, false)
	}
}

func BenchmarkExpandedTriggerInputs(b *testing.B) {
	ctx := b.Context()
	graph, state := newTestGraph(b, b.TempDir(), benchManifest())
	s := newTestStepDef(b, graph, state, "t")
	if n := len(s.ExpandedTriggerInputs(ctx)); n != 2000+51*20 {
		b.Fatalf("ExpandedTriggerInputs returned %d inputs; want %d", n, 2000+51*20)
	}
	b.ReportAllocs()
	for b.Loop() {
		s.ExpandedTriggerInputs(ctx)
	}
}

// bigStepManifest returns a manifest where step "big" has 25000 direct
// inputs and 250 phony inputs of 100 inputs each, above maxSmallDedupSet.
// If files is true, it also writes the input files in dir.
func bigStepManifest(tb testing.TB, dir string, files bool) string {
	tb.Helper()
	var b strings.Builder
	b.WriteString("rule r\n  command = x\n")
	var ins []string
	for i := range 25000 {
		ins = append(ins, fmt.Sprintf("../../src/big/f%d.o", i))
	}
	for i := range 250 {
		var grp []string
		for j := range 100 {
			grp = append(grp, fmt.Sprintf("../../src/g%d/f%d.o", i, j))
		}
		fmt.Fprintf(&b, "build g%d: phony %s\n", i, strings.Join(grp, " "))
		ins = append(ins, grp...)
	}
	if files {
		for _, in := range ins {
			fname := filepath.Join(dir, "out/Default", in)
			if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
				tb.Fatal(err)
			}
			if err := os.WriteFile(fname, nil, 0644); err != nil {
				tb.Fatal(err)
			}
		}
	}
	ins = ins[:25000]
	for i := range 250 {
		ins = append(ins, fmt.Sprintf("g%d", i))
	}
	fmt.Fprintf(&b, "build big: r %s\n", strings.Join(ins, " "))
	return b.String()
}

// newBigStepDef returns the StepDef of "big" with stepConfig sc.
func newBigStepDef(tb testing.TB, sc *StepConfig, files bool) *StepDef {
	tb.Helper()
	dir := tb.TempDir()
	graph, state := newTestGraph(tb, dir, bigStepManifest(tb, dir, files))
	if err := sc.Init(tb.Context()); err != nil {
		tb.Fatal(err)
	}
	graph.globals.stepConfig = sc
	return newTestStepDef(tb, graph, state, "big")
}

func BenchmarkDepsBaseInputs_big(b *testing.B) {
	ctx := b.Context()
	s := newBigStepDef(b, &StepConfig{}, false)
	if n := len(s.DepsBaseInputs(ctx, nil, false)); n != 50250 {
		b.Fatalf("DepsBaseInputs returned %d inputs; want %d", n, 50250)
	}
	b.ReportAllocs()
	for b.Loop() {
		s.DepsBaseInputs(ctx, nil, false)
	}
}

func BenchmarkExpandedTriggerInputs_big(b *testing.B) {
	ctx := b.Context()
	s := newBigStepDef(b, &StepConfig{}, false)
	if n := len(s.ExpandedTriggerInputs(ctx)); n != 50000 {
		b.Fatalf("ExpandedTriggerInputs returned %d inputs; want %d", n, 50000)
	}
	b.ReportAllocs()
	for b.Loop() {
		s.ExpandedTriggerInputs(ctx)
	}
}

func BenchmarkExpandedInputs_big(b *testing.B) {
	for _, indirect := range []bool{false, true} {
		b.Run(fmt.Sprintf("indirect=%t", indirect), func(b *testing.B) {
			ctx := b.Context()
			sc := &StepConfig{}
			if indirect {
				sc.Rules = []*StepRule{{
					Name:           "big",
					ActionName:     "^r$",
					IndirectInputs: &PathFilter{},
				}}
			}
			s := newBigStepDef(b, sc, true)
			if s.rule.IndirectInputs.enabled() != indirect {
				b.Fatalf("indirect_inputs enabled=%t; want %t", !indirect, indirect)
			}
			ins := s.ExpandedInputs(ctx)
			if n := len(ins); n != 50000 {
				b.Fatalf("ExpandedInputs returned %d inputs; want %d", n, 50000)
			}
			// Compute the digests that the first Stats queued, so that
			// the loop doesn't time them.
			if _, err := s.globals.hashFS.Entries(ctx, s.globals.path.WorkspaceRoot, ins); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				s.ExpandedInputs(ctx)
			}
		})
	}
}
