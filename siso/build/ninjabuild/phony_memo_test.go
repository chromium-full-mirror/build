// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjabuild

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	saved := minInputGroupPaths
	minInputGroupPaths = 2
	t.Cleanup(func() { minInputGroupPaths = saved })
	ngroups := 0
	defer func() {
		if ngroups == 0 {
			t.Errorf("no input groups were tested")
		}
	}()
	for seed := int64(1); seed <= 40; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		dir := t.TempDir()
		var b strings.Builder
		b.WriteString("rule r\n  command = x\n")
		nsrc := 30
		for i := range nsrc {
			if rnd.Intn(3) == 0 {
				if err := os.MkdirAll(filepath.Join(dir, "src"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "src", fmt.Sprintf("f%d", i)), nil, 0644); err != nil {
					t.Fatal(err)
				}
			}
		}
		// gen%d are outputs of non-phony edges.
		const ngen = 5
		for i := range ngen {
			fmt.Fprintf(&b, "build gen%d: r ../../src/f%d\n", i, i)
		}
		nphony := 25
		ref := func(max int) string {
			switch r := rnd.Intn(10); {
			case r < 5 || max == 0:
				return fmt.Sprintf("../../src/f%d", rnd.Intn(nsrc))
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
				manifest += fmt.Sprintf("build ph%d_b: phony ../../src/f%d\n", i, i%nsrc)
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
			// ExpandedTriggerInputGroups: same set of paths.
			wantSet := map[string]bool{}
			for _, p := range want {
				wantSet[p] = true
			}
			for name, step := range map[string]*StepDef{"memo": s, "reference": refStep} {
				own, groups := step.ExpandedTriggerInputGroups(ctx)
				gotSet := map[string]bool{}
				for _, p := range own {
					gotSet[string(p)] = true
				}
				for _, g := range groups {
					for _, p := range g.Paths {
						gotSet[string(p)] = true
					}
				}
				if diff := cmp.Diff(wantSet, gotSet); diff != "" {
					t.Fatalf("seed %d t%d: ExpandedTriggerInputGroups set diff -ref +%s:\n%s\nmanifest:\n%s", seed, i, name, diff, manifest)
				}
				if step == s {
					ngroups += len(groups)
				}
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

// TestExpandedTriggerInputGroups_samePath checks phony edges with two
// nodes of the same target path, within the group or between the group
// and the step's other inputs. The flat expansion skips one of them as
// seen, so such an edge is not a group.
func TestExpandedTriggerInputGroups_samePath(t *testing.T) {
	saved := minInputGroupPaths
	minInputGroupPaths = 2
	t.Cleanup(func() { minInputGroupPaths = saved })
	for _, tc := range []struct {
		name     string
		manifest string
	}{
		{
			// within the group.
			name: "phony_in_group",
			manifest: `build ../../tc/s.h ph_b: phony ../../tc/x0.h ../../tc/x1.h
build grp: phony ../../tc/f0.h ../../tc/f1.h ../../tc/./s.h ../../tc/s.h
build out: r | grp ph_b
`,
		},
		{
			// a phony in the group has the path of an own input.
			name: "phony_in_group_own_input",
			manifest: `build ../../tc/./s.h: phony ../../tc/x0.h ../../tc/x1.h
build grp: phony ../../tc/f0.h ../../tc/f1.h ../../tc/./s.h
build out: r ../../tc/s.h | grp
`,
		},
		{
			// a clean path that leaves the build dir and comes back in
			// has the path of an own input.
			name: "phony_in_group_via_build_dir",
			manifest: `build ../Default/ph: phony ../../tc/x0.h ../../tc/x1.h
build grp: phony ../../tc/f0.h ../../tc/f1.h ../Default/ph
build out: r ph | grp
`,
		},
		{
			// a directory target.
			name: "phony_dir_in_group_own_input",
			manifest: `build ../../tc/./dir/: phony ../../tc/x0.h ../../tc/x1.h
build grp: phony ../../tc/f0.h ../../tc/f1.h ../../tc/./dir/
build out: r ../../tc/dir/ | grp
`,
		},
		{
			// a group leaf has the path of a later phony input.
			name: "leaf_later_phony",
			manifest: `build grp: phony ../../tc/f0.h ../../tc/f1.h
build ../../tc/./f0.h: phony ../../tc/y0.h ../../tc/y1.h
build out: r | grp ../../tc/./f0.h
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			graph, state := newTestGraph(t, t.TempDir(), "rule r\n  command = x\n"+tc.manifest)
			s := newTestStepDef(t, graph, state, "out")
			want := path.Strings(s.ExpandedTriggerInputs(ctx))
			own, groups := s.ExpandedTriggerInputGroups(ctx)
			got := path.Strings(own)
			for _, g := range groups {
				if g.Name == "out/Default/grp" {
					t.Errorf("grp is a group: %v", g.Paths)
				}
				got = append(got, path.Strings(g.Paths)...)
			}
			slices.Sort(want)
			slices.Sort(got)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("ExpandedTriggerInputGroups paths diff -flat +own and groups:\n%s", diff)
			}
		})
	}
}

func TestCanonicalNodePath(t *testing.T) {
	base := []string{"out", "Default"}
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"x.o", true},
		{"gen/x.h", true},
		{"dir/", true},
		{"..", true},
		{"../tc/s.h", true},
		{"../../tc/s.h", true},
		{"../../../ws/x.h", true},
		{"./x.o", false},
		{"a//b", false},
		{"../../tc/./s.h", false},
		{"../../tc/./dir/", false},
		{"../Default/ph", false},
		{"../../out/tc/s.h", false},
	} {
		if got := canonicalNodePath(base, tc.raw); got != tc.want {
			t.Errorf("canonicalNodePath(%q, %q)=%t; want %t", base, tc.raw, got, tc.want)
		}
	}
	// "/x.h" is absolute except on Windows, where targetPath joins it to
	// the build dir.
	if got, want := canonicalNodePath(base, "/x.h"), runtime.GOOS != "windows"; got != want {
		t.Errorf("canonicalNodePath(%q, %q)=%t; want %t", base, "/x.h", got, want)
	}
}

// TestPhonyGroupPathsBounded checks that a group whose paths don't fit in
// maxGroupPaths is expanded as plain paths.
func TestPhonyGroupPathsBounded(t *testing.T) {
	ctx := t.Context()
	saved := minInputGroupPaths
	minInputGroupPaths = 2
	t.Cleanup(func() { minInputGroupPaths = saved })
	graph, state := newTestGraph(t, t.TempDir(), `rule r
  command = x
build grp: phony ../../tc/f0.h ../../tc/f1.h
build out: r | grp
`)
	graph.globals.groupPaths.Store(maxGroupPaths - 1)
	s := newTestStepDef(t, graph, state, "out")
	own, groups := s.ExpandedTriggerInputGroups(ctx)
	if len(groups) != 0 {
		t.Errorf("groups=%v; want none", groups)
	}
	if want := path.Strings(s.ExpandedTriggerInputs(ctx)); !slices.Equal(path.Strings(own), want) {
		t.Errorf("own=%q; want %q", own, want)
	}
	if n := graph.globals.groupPaths.Load(); n != maxGroupPaths-1 {
		t.Errorf("groupPaths=%d; want %d", n, maxGroupPaths-1)
	}
}

// TestPhonyGroupRejectedNoBudget checks that phony edges whose paths
// GroupDir rejects don't use up maxGroupPaths.
func TestPhonyGroupRejectedNoBudget(t *testing.T) {
	ctx := t.Context()
	saved := minInputGroupPaths
	minInputGroupPaths = 2
	t.Cleanup(func() { minInputGroupPaths = saved })
	graph, state := newTestGraph(t, t.TempDir(), `rule r
  command = x
build dirs: phony ../../tc/d/ ../../tc/e/
build above: phony ../../../sdk/f0.h ../../../sdk/f1.h
build out: r | dirs above
`)
	s := newTestStepDef(t, graph, state, "out")
	if _, groups := s.ExpandedTriggerInputGroups(ctx); len(groups) != 0 {
		t.Errorf("groups=%v; want none", groups)
	}
	if n := graph.globals.groupPaths.Load(); n != 0 {
		t.Errorf("groupPaths=%d; want 0", n)
	}
}
