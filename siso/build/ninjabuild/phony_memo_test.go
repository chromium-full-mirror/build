// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjabuild

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/toolsupport/ninjautil"
)

// TestExpandedTriggerInputs_phonyMemo checks that the memoized phony
// expansion gives exactly the same inputs, in the same order, as
// replacePhony on random graphs of nested, shared, empty and multi-output
// phony edges and generated inputs, with steps expanded in random order
// sharing one memo. The reference steps are debug steps, which expandPhony
// sends to replacePhony.
func TestExpandedTriggerInputs_phonyMemo(t *testing.T) {
	ctx := t.Context()
	var nmemo int
	for seed := int64(1); seed <= 40; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		dir := t.TempDir()
		var b strings.Builder
		b.WriteString("rule r\n  command = x\n")
		nsrc := 30
		for i := range nsrc {
			if rnd.Intn(3) == 0 {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("src%d", i)), nil, 0644); err != nil {
					t.Fatal(err)
				}
			}
		}
		// gen%d are outputs of non-phony edges.
		const ngen = 5
		for i := range ngen {
			fmt.Fprintf(&b, "build gen%d: r ../../src%d\n", i, i)
		}
		nphony := 25
		ref := func(max int) string {
			switch r := rnd.Intn(10); {
			case r < 5 || max == 0:
				return fmt.Sprintf("../../src%d", rnd.Intn(nsrc))
			case r == 5:
				return fmt.Sprintf("gen%d", rnd.Intn(ngen))
			default:
				return fmt.Sprintf("ph%d", rnd.Intn(max))
			}
		}
		for i := range nphony {
			outs := fmt.Sprintf("ph%d", i)
			if rnd.Intn(6) == 0 {
				// multi-output phony
				outs += fmt.Sprintf(" ph%d_b", i)
			}
			if rnd.Intn(5) == 0 {
				// empty phony; some exist as files.
				if rnd.Intn(2) == 0 {
					if err := os.MkdirAll(filepath.Join(dir, "out/Default"), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "out/Default", fmt.Sprintf("ph%d", i)), nil, 0644); err != nil {
						t.Fatal(err)
					}
				}
				fmt.Fprintf(&b, "build %s: phony\n", outs)
				continue
			}
			var ins []string
			for k := rnd.Intn(6) + 1; k > 0; k-- {
				ins = append(ins, ref(i))
				if i > 0 && rnd.Intn(8) == 0 {
					ins = append(ins, fmt.Sprintf("ph%d_b", rnd.Intn(i)))
				}
			}
			fmt.Fprintf(&b, "build %s: phony %s\n", outs, strings.Join(ins, " "))
		}
		nsteps := 30
		for i := range nsteps {
			var ins []string
			for k := rnd.Intn(8) + 1; k > 0; k-- {
				ins = append(ins, ref(nphony))
			}
			oo := ""
			if rnd.Intn(2) == 0 {
				oo = " || " + ref(nphony)
			}
			fmt.Fprintf(&b, "build t%d: r %s%s\n", i, strings.Join(ins, " "), oo)
		}
		// "ph%d_b" references to single-output phonies are invalid; declare them.
		manifest := b.String()
		for i := range nphony {
			if strings.Contains(manifest, fmt.Sprintf("ph%d_b", i)) && !strings.Contains(manifest, fmt.Sprintf("build ph%d ph%d_b:", i, i)) {
				manifest += fmt.Sprintf("build ph%d_b: phony ../../src%d\n", i, i%nsrc)
			}
		}
		fname := filepath.Join(dir, "build.ninja")
		if err := os.WriteFile(fname, []byte(manifest), 0644); err != nil {
			t.Fatal(err)
		}
		state := ninjautil.NewState()
		if err := ninjautil.NewManifestParser(state).Load(ctx, fname); err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, manifest)
		}
		hashFS, err := hashfs.New(ctx, hashfs.Option{})
		if err != nil {
			t.Fatal(err)
		}
		newGraph := func() *Graph {
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
				t.Fatal(err)
			}
			return graph
		}
		graph := newGraph()
		refGraph := newGraph()
		for _, i := range rnd.Perm(nsteps) {
			node, ok := state.LookupNodeByPath(fmt.Sprintf("t%d", i))
			if !ok {
				t.Fatalf("t%d not found", i)
			}
			edge, _ := node.InEdge()
			s := graph.newStepDef(ctx, edge, nil)
			s.EnsureRule(ctx)
			refStep := refGraph.newStepDef(ctx, edge, nil)
			refStep.EnsureRule(ctx)
			refStep.rule.Debug = true
			for _, oo := range []bool{false, true} {
				want := path.Strings(refStep.DepsBaseInputs(ctx, nil, oo))
				got := path.Strings(s.DepsBaseInputs(ctx, nil, oo))
				if diff := cmp.Diff(want, got); diff != "" {
					t.Fatalf("seed %d t%d: DepsBaseInputs(%t) diff -ref +memo:\n%s\nmanifest:\n%s", seed, i, oo, diff, manifest)
				}
			}
			want := path.Strings(refStep.ExpandedTriggerInputs(ctx))
			got := path.Strings(s.ExpandedTriggerInputs(ctx))
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("seed %d t%d: ExpandedTriggerInputs diff -ref +memo:\n%s\nmanifest:\n%s", seed, i, diff, manifest)
			}
		}
		refGraph.globals.phonyMemo.Range(func(k, v any) bool {
			t.Fatalf("seed %d: memo of the reference graph has an entry", seed)
			return false
		})
		graph.globals.phonyMemo.Range(func(k, v any) bool {
			nmemo++
			return true
		})
		hashFS.Close(ctx)
	}
	if nmemo == 0 {
		t.Errorf("memo was not used")
	}
}

// TestPhonyExpansion_concurrent checks that concurrent callers share one
// expansion per edge.
func TestPhonyExpansion_concurrent(t *testing.T) {
	var b strings.Builder
	b.WriteString("rule r\n  command = x\n")
	const nphony = 20
	for i := range nphony {
		var ins []string
		for j := range 10 {
			ins = append(ins, fmt.Sprintf("../../src/p%d/f%d.h", i, j))
		}
		if i > 0 {
			ins = append(ins, fmt.Sprintf("ph%d", i-1))
		}
		fmt.Fprintf(&b, "build ph%d: phony %s\n", i, strings.Join(ins, " "))
	}
	graph, state := newTestGraph(t, t.TempDir(), b.String())
	var edges []*ninjautil.Edge
	for i := range nphony {
		node, ok := state.LookupNodeByPath(fmt.Sprintf("ph%d", i))
		if !ok {
			t.Fatalf("ph%d not found", i)
		}
		edge, _ := node.InEdge()
		edges = append(edges, edge)
	}
	// In a build, the target paths of a step's inputs are set before the
	// step runs, so concurrent expansions only read them.
	for _, n := range state.AllNodes() {
		graph.globals.targetPath(n)
	}
	got := make([][]*phonyExpansion, 16)
	var wg sync.WaitGroup
	for g := range got {
		got[g] = make([]*phonyExpansion, nphony)
		wg.Go(func() {
			for _, i := range rand.Perm(nphony) {
				got[g][i] = graph.globals.phonyExpansionOf(graph.globals.phonyMemoEntry(edges[i]))
			}
		})
	}
	wg.Wait()
	for i := range nphony {
		exp := got[0][i]
		if exp == nil {
			t.Errorf("ph%d: no expansion", i)
		}
		for g := range got {
			if got[g][i] != exp {
				t.Errorf("ph%d: goroutines got different expansions", i)
			}
		}
	}
}
