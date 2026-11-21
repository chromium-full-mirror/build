// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"go.chromium.org/build/siso/execute"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
)

// groupStepDef is a StepDef whose trigger inputs are own paths and input
// groups (ExpandedTriggerInputGroups) or the flat list
// (ExpandedTriggerInputs).
type groupStepDef struct {
	fakeStepDef
	own    []path.Path
	groups []execute.InputGroup
	flat   []path.Path
}

func (d groupStepDef) String() string { return "groupStepDef" }

func (d groupStepDef) ExpandedTriggerInputs(context.Context) []path.Path { return d.flat }

func (d groupStepDef) ExpandedTriggerInputGroups(context.Context) ([]path.Path, []execute.InputGroup) {
	return d.own, d.groups
}

// TestComputeLookupKeyVerifiesInputGroups checks the verify-on-use policy:
// the first keys of a group are compared with the flat key; a mismatch
// (here an expansion that drops a path) returns the flat key and turns
// input groups off for the rest of the build.
func TestComputeLookupKeyVerifiesInputGroups(t *testing.T) {
	if os.Getenv("SISO_VERIFY_INPUT_GROUPS") != "" {
		t.Skip("SISO_VERIFY_INPUT_GROUPS verifies every key")
	}
	SetExperimentForTest("expand-phony-trigger-inputs")
	t.Cleanup(func() { SetExperimentForTest("") })
	ctx := t.Context()
	dir := t.TempDir()
	var toolchain []path.Path
	for i := range 20 {
		f := fmt.Sprintf("prebuilts/clang/lib/sub%d/h%d.h", i%3, i)
		toolchain = append(toolchain, path.Path(f))
	}
	for _, f := range append(path.Strings(toolchain), "src/a.cc", "src/b.h", "prebuilts/clang/lib/extra.txt") {
		fname := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fname, []byte(f), 0644); err != nil {
			t.Fatal(err)
		}
	}
	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hashFS.Close(ctx)
	if err := hashFS.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	newStep := func(def StepDef) *Step {
		return &Step{
			def: def,
			cmd: &execute.Cmd{
				ID:            "step",
				Args:          []string{"clang", "-c", "src/a.cc"},
				WorkspaceRoot: dir,
				WorkDir:       "out",
				HashFS:        hashFS,
			},
		}
	}
	flatKey := func(inputs []path.Path) string {
		t.Helper()
		c := newStep(nil).cmd
		c.Inputs = inputs
		c.Pure = true
		d, err := c.Digest(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		return d.String()
	}
	group := execute.InputGroup{Key: "tc", Name: "tc", Paths: toolchain}
	newRT := func(outDir path.Path) (reapiTwoPhaseCaching, *execute.InputGroupCache) {
		gc := execute.NewInputGroupCache(outDir)
		return reapiTwoPhaseCaching{b: &Builder{inputGroups: gc}}, gc
	}

	t.Run("equal", func(t *testing.T) {
		rt, gc := newRT("")
		own := []path.Path{"src/a.cc", "src/b.h"}
		def := groupStepDef{own: own, groups: []execute.InputGroup{group}, flat: append(append([]path.Path{}, own...), toolchain...)}
		want := flatKey(def.flat)
		for i := range 20 {
			got, err := rt.ComputeLookupKey(ctx, newStep(def))
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("key %d = %s; want %s", i, got, want)
			}
		}
		st := gc.Stats()
		if st.Verified != 8 || st.Mismatches != 0 {
			t.Errorf("verified %d (mismatches %d); want 8 (0)", st.Verified, st.Mismatches)
		}
		if st.Spliced != 20 || st.Fallbacks != 0 {
			t.Errorf("spliced %d, fallbacks %d; want 20, 0", st.Spliced, st.Fallbacks)
		}
		if gc.Broken() {
			t.Errorf("Broken()=true; want false")
		}
	})

	t.Run("fallbacks_dont_use_first_verifications", func(t *testing.T) {
		rt, gc := newRT("")
		// An own input in the group dir that is not a member makes the
		// digest fall back to the flat computation.
		fown := []path.Path{"src/a.cc", "prebuilts/clang/lib/extra.txt"}
		fdef := groupStepDef{own: fown, groups: []execute.InputGroup{group}, flat: append(append([]path.Path{}, fown...), toolchain...)}
		fwant := flatKey(fdef.flat)
		for i := range 20 {
			got, err := rt.ComputeLookupKey(ctx, newStep(fdef))
			if err != nil {
				t.Fatal(err)
			}
			if got != fwant {
				t.Fatalf("fallback key %d = %s; want %s", i, got, fwant)
			}
		}
		if st := gc.Stats(); st.Verified != 0 || st.Spliced != 0 || st.Fallbacks != 20 {
			t.Errorf("after fallbacks: verified=%d spliced=%d fallbacks=%d; want 0 0 20", st.Verified, st.Spliced, st.Fallbacks)
		}
		own := []path.Path{"src/a.cc", "src/b.h"}
		def := groupStepDef{own: own, groups: []execute.InputGroup{group}, flat: append(append([]path.Path{}, own...), toolchain...)}
		for range 10 {
			if _, err := rt.ComputeLookupKey(ctx, newStep(def)); err != nil {
				t.Fatal(err)
			}
		}
		if st := gc.Stats(); st.Verified != 8 {
			t.Errorf("verified %d of the spliced keys; want the first 8", st.Verified)
		}
	})

	t.Run("build_dir_excluded", func(t *testing.T) {
		own := []path.Path{"src/a.cc", "src/b.h"}
		def := groupStepDef{own: own, groups: []execute.InputGroup{group}, flat: append(append([]path.Path{}, own...), toolchain...)}
		want := flatKey(def.flat)
		for _, outDir := range []path.Path{"prebuilts", "prebuilts/clang/lib", "prebuilts/clang"} {
			rt, gc := newRT(outDir)
			// the second key skips the unusable group.
			for range 2 {
				got, err := rt.ComputeLookupKey(ctx, newStep(def))
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("outDir %s: key %s; want %s", outDir, got, want)
				}
			}
			if st := gc.Stats(); st.Spliced != 0 || st.Fallbacks != 2 {
				t.Errorf("outDir %s: spliced %d, fallbacks %d; want 0, 2", outDir, st.Spliced, st.Fallbacks)
			}
		}
		rt, gc := newRT("out")
		if _, err := rt.ComputeLookupKey(ctx, newStep(def)); err != nil {
			t.Fatal(err)
		}
		if st := gc.Stats(); st.Spliced != 1 {
			t.Errorf("outDir out: subtree used %d times; want 1", st.Spliced)
		}
	})

	t.Run("expansion_mismatch", func(t *testing.T) {
		rt, gc := newRT("")
		// The grouped expansion misses src/b.h.
		own := []path.Path{"src/a.cc"}
		flat := append([]path.Path{"src/a.cc", "src/b.h"}, toolchain...)
		def := groupStepDef{own: own, groups: []execute.InputGroup{group}, flat: flat}
		want := flatKey(flat)
		for i := range 3 {
			got, err := rt.ComputeLookupKey(ctx, newStep(def))
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("key %d = %s; want flat %s", i, got, want)
			}
		}
		if st := gc.Stats(); st.Mismatches != 1 {
			t.Errorf("mismatches %d; want 1 (groups off after the first)", st.Mismatches)
		}
		if !gc.Broken() {
			t.Errorf("Broken()=false; want true")
		}
		// another Builder still uses groups.
		ort, ogc := newRT("")
		gdef := groupStepDef{own: []path.Path{"src/a.cc", "src/b.h"}, groups: []execute.InputGroup{group}, flat: flat}
		if _, err := ort.ComputeLookupKey(ctx, newStep(gdef)); err != nil {
			t.Fatal(err)
		}
		if st := ogc.Stats(); st.Spliced != 1 || ogc.Broken() {
			t.Errorf("other builder: spliced %d, broken %t; want 1, false", st.Spliced, ogc.Broken())
		}
	})
}
