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
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/toolsupport/ninjautil"
)

// randomPhonyManifest writes some source files and empty phony targets in
// dir, and returns a manifest with nested, shared, empty and multi-output
// phony edges, and nsteps steps "t<i>" using them as inputs and
// order-only inputs.
func randomPhonyManifest(t *testing.T, rnd *rand.Rand, dir string, nsteps int) string {
	t.Helper()
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
	nphony := 25
	ref := func(max int) string {
		switch r := rnd.Intn(10); {
		case r < 5 || max == 0:
			return fmt.Sprintf("../../src%d", rnd.Intn(nsrc))
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
	return manifest
}

// depsBaseInputsRef is DepsBaseInputs as it was before it was optimized
// (without debug logs), used as the reference.
func depsBaseInputsRef(t *testing.T, s *StepDef, toolInputs []path.Path, includeOrderOnly bool) []path.Path {
	t.Helper()
	ctx := t.Context()
	var inputs []path.Path
	inputs = append(inputs, toolInputs...)
	filter := s.globals.stepConfig.Scandeps.stepInputsFilter
	seen := make(map[path.Path]bool)
	var stepInputs []path.Path
	var nodes []*ninjautil.Node
	if includeOrderOnly {
		stepInputs = s.Inputs(ctx)
		nodes = s.edge.Inputs()
	} else {
		stepInputs = s.TriggerInputs(ctx)
		nodes = s.edge.TriggerInputs()
	}
	visited := make(map[*ninjautil.Edge]bool)
	for _, in := range nodes {
		p := s.globals.targetPath(in)
		if inEdge, ok := in.InEdge(); ok && inEdge.IsPhony() {
			stepInputs = replacePhony(ctx, s.globals, visited, seen, p, inEdge, false, stepInputs)
		}
	}
	seen = make(map[path.Path]bool)
	for _, in := range stepInputs {
		if seen[in] {
			continue
		}
		seen[in] = true
		if !filter(ctx, string(in), false) {
			continue
		}
		inputs = append(inputs, in)
	}
	return inputs
}

// TestDepsBaseInputs_random checks that DepsBaseInputs returns exactly the
// same inputs, in the same order, as the reference implementation, on
// random graphs, with steps expanded in random order on one graph, with
// the default step_inputs filter and with one that excludes some paths.
func TestDepsBaseInputs_random(t *testing.T) {
	ctx := t.Context()
	excludeConfig := &StepConfig{
		Scandeps: &ScandepsConfig{
			StepInputs: PathFilter{Excludes: []string{"*5"}},
		},
	}
	if err := excludeConfig.Init(ctx); err != nil {
		t.Fatal(err)
	}
	excluded := 0
	for seed := int64(1); seed <= 40; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		dir := t.TempDir()
		const nsteps = 30
		manifest := randomPhonyManifest(t, rnd, dir, nsteps)
		graph, state := newTestGraph(t, dir, manifest)
		defaultConfig := graph.globals.stepConfig
		for _, i := range rnd.Perm(nsteps) {
			s := newTestStepDef(t, graph, state, fmt.Sprintf("t%d", i))
			for _, oo := range []bool{false, true} {
				for _, toolInputs := range [][]path.Path{nil, {"tool/bin", "src1"}} {
					var nwant []int
					for _, sc := range []*StepConfig{defaultConfig, excludeConfig} {
						graph.globals.stepConfig = sc
						want := path.Strings(depsBaseInputsRef(t, s, toolInputs, oo))
						got := path.Strings(s.DepsBaseInputs(ctx, toolInputs, oo))
						if diff := cmp.Diff(want, got); diff != "" {
							t.Fatalf("seed %d t%d: DepsBaseInputs(%q, %t) step_inputs=%v diff -ref +got:\n%s\nmanifest:\n%s", seed, i, toolInputs, oo, sc.Scandeps.StepInputs, diff, manifest)
						}
						nwant = append(nwant, len(want))
					}
					if nwant[1] < nwant[0] {
						excluded++
					}
				}
			}
		}
	}
	if excluded == 0 {
		t.Errorf("step_inputs excludes never dropped an input")
	}
}
