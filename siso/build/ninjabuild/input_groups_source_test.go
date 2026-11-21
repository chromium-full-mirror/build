// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjabuild

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/toolsupport/ninjautil"
)

// TestExpandedTriggerInputGroups_sourceOnly checks that only phony
// expansions of source files become input groups: an expansion with a
// file that a step writes is merged into the own inputs.
func TestExpandedTriggerInputGroups_sourceOnly(t *testing.T) {
	ctx := t.Context()
	saved := minInputGroupPaths
	minInputGroupPaths = 2
	t.Cleanup(func() { minInputGroupPaths = saved })
	dir := t.TempDir()
	manifest := `rule r
  command = x
build ../../src/gen.h: r ../../src/a.cc
build tc: phony ../../tc/bin/clang ../../tc/lib/a.h ../../tc/lib/b.h
build hdrs: phony ../../src/gen.h ../../src/b.h ../../src/c.h
build t.o: r ../../src/a.cc | tc hdrs
build hdrs2: phony tc ../../src/gen.h
build u.o: r ../../src/a.cc | tc hdrs2
`
	fname := filepath.Join(dir, "build.ninja")
	if err := os.WriteFile(fname, []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	state := ninjautil.NewState()
	if err := ninjautil.NewManifestParser(state).Load(ctx, fname); err != nil {
		t.Fatal(err)
	}
	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hashFS.Close(ctx)
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
	node, ok := state.LookupNodeByPath("t.o")
	if !ok {
		t.Fatal("t.o not found")
	}
	edge, _ := node.InEdge()
	s := graph.newStepDef(ctx, edge, nil)
	s.EnsureRule(ctx)
	own, groups := s.ExpandedTriggerInputGroups(ctx)
	if len(groups) != 1 || groups[0].Name != "out/Default/tc" {
		var names []string
		for _, g := range groups {
			names = append(names, g.Name)
		}
		t.Fatalf("groups %q; want only the source-only phony out/Default/tc", names)
	}
	for _, p := range []path.Path{"src/gen.h", "src/b.h", "src/c.h"} {
		if !slices.Contains(own, p) {
			t.Errorf("own inputs %q don't have %s of the phony with a generated file", own, p)
		}
	}
	// a later phony that includes the group: the group's paths are not
	// in own.
	node, ok = state.LookupNodeByPath("u.o")
	if !ok {
		t.Fatal("u.o not found")
	}
	edge, _ = node.InEdge()
	s = graph.newStepDef(ctx, edge, nil)
	s.EnsureRule(ctx)
	own, groups = s.ExpandedTriggerInputGroups(ctx)
	if len(groups) != 1 || groups[0].Name != "out/Default/tc" {
		t.Fatalf("u.o: groups %v; want out/Default/tc", groups)
	}
	if want := []path.Path{"src/a.cc", "src/gen.h"}; !slices.Equal(own, want) {
		t.Errorf("u.o: own %q; want %q", own, want)
	}
}
