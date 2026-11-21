// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjabuild

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/execute"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/toolsupport/ninjautil"
)

// TestExpandedTriggerInputGroups_digest checks on a small ninja graph that
// the two-phase cache key digest computed from ExpandedTriggerInputGroups
// (own inputs and input groups) is the same as the one computed from
// ExpandedTriggerInputs, for steps whose groups are used as subtrees and
// steps that fall back to the flat computation.
func TestExpandedTriggerInputGroups_digest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses symlinks")
	}
	ctx := t.Context()
	saved := minInputGroupPaths
	minInputGroupPaths = 2
	t.Cleanup(func() { minInputGroupPaths = saved })
	gc := execute.NewInputGroupCache("out/Default")

	dir := t.TempDir()
	write := func(f string) {
		t.Helper()
		fname := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fname, []byte(f), 0644); err != nil {
			t.Fatal(err)
		}
	}
	symlink := func(target, f string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{
		"tc/bin/clang", "tc/lib/a.h", "tc/lib/sub/b.h", "tc/lib/sub/c.h", "tc/share/x.txt", "tc/lib/extra.h",
		"src/a.cc", "src/b.cc", "src/c.cc", "src/h1.h", "src/h2.h", "src/g.in",
		"out/Default/gen/g.h",
	} {
		write(f)
	}
	symlink("clang", "tc/bin/clang++")
	symlink("a.cc", "src/link.cc")

	manifest := `rule cc
  command = clang -c $in -o $out
rule gen
  command = gen $out
build gen/g.h: gen ../../src/g.in
build tc: phony ../../tc/bin/clang ../../tc/bin/clang++ ../../tc/lib/a.h ../../tc/lib/sub/b.h ../../tc/lib/sub/c.h
build tcx: phony tc ../../tc/share/x.txt
build hdrs: phony gen/g.h ../../src/h1.h ../../src/h2.h
build hdrsx: phony tc gen/g.h
build obj/a.o: cc ../../src/a.cc | tc hdrs
build obj/b.o: cc ../../src/b.cc | tcx ../../src/h1.h
build obj/c.o: cc ../../src/c.cc | tc tcx ../../tc/lib/a.h
build obj/d.o: cc ../../src/link.cc | tc
build obj/e.o: cc ../../tc/lib/extra.h ../../src/a.cc | tc
build obj/f.o: cc ../../src/a.cc | tc hdrsx
`
	fname := filepath.Join(dir, "out/Default/build.ninja")
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
	if err := hashFS.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
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
	var spliced []string
	for _, out := range []string{"obj/a.o", "obj/b.o", "obj/c.o", "obj/d.o", "obj/e.o", "obj/f.o"} {
		node, ok := state.LookupNodeByPath(out)
		if !ok {
			t.Fatalf("%s not found", out)
		}
		edge, _ := node.InEdge()
		s := graph.newStepDef(ctx, edge, nil)
		s.EnsureRule(ctx)
		own, groups := s.ExpandedTriggerInputGroups(ctx)
		flat := s.ExpandedTriggerInputs(ctx)
		newCmd := func() *execute.Cmd {
			return &execute.Cmd{
				ID:            out,
				Args:          s.Args(ctx),
				WorkspaceRoot: dir,
				WorkDir:       "out/Default",
				Pure:          true,
				HashFS:        hashFS,
			}
		}
		c := newCmd()
		c.Inputs = own
		c.InputGroups = groups
		c.InputGroupCache = gc
		got, err := c.Digest(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		fc := newCmd()
		fc.Inputs = flat
		want, err := fc.Digest(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			//goanalyzer:ignore rawstring failure message format
			t.Errorf("%s: digest with groups %s != flat %s\nown %q\ngroups %v\nflat %q", out, got, want, own, groups, flat)
		}
		if c.InputGroupsSpliced() {
			spliced = append(spliced, out)
		}
	}
	// c.o has two groups in the same dir, d.o an own symlink input and
	// e.o an own input in the group dir that isn't in the group.
	if want := []string{"obj/a.o", "obj/b.o", "obj/f.o"}; !slices.Equal(spliced, want) {
		t.Errorf("subtrees used for %q; want %q", spliced, want)
	}
}
