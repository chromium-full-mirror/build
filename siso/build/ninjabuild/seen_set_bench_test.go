// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjabuild

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"go.chromium.org/build/siso/path"
)

// compileManifest has compile-like steps:
//   - "obj/c%d.o": 6 sources plus "toolchain" (20 groups x 10 subgroups x
//     100 leaves = 20000 leaves) and "sysroot" (5 groups x 1000 leaves).
//   - "obj/s%d.o": 6 sources plus "gen" (a phony of 50 leaves).
//   - "big": 25000 direct inputs and 250 phony groups of 100 leaves.
func compileManifest() string {
	var b strings.Builder
	b.WriteString("rule r\n  command = x\n")
	var tc []string
	for g := range 20 {
		var sub []string
		for s := range 10 {
			var leaves []string
			for l := range 100 {
				leaves = append(leaves, fmt.Sprintf("../../tc/g%d/s%d/f%d.h", g, s, l))
			}
			fmt.Fprintf(&b, "build tc_g%d_s%d: phony %s\n", g, s, strings.Join(leaves, " "))
			sub = append(sub, fmt.Sprintf("tc_g%d_s%d", g, s))
		}
		fmt.Fprintf(&b, "build tc_g%d: phony %s\n", g, strings.Join(sub, " "))
		tc = append(tc, fmt.Sprintf("tc_g%d", g))
	}
	fmt.Fprintf(&b, "build toolchain: phony %s\n", strings.Join(tc, " "))
	var sr []string
	for g := range 5 {
		var leaves []string
		for l := range 1000 {
			leaves = append(leaves, fmt.Sprintf("../../sysroot/g%d/f%d.h", g, l))
		}
		fmt.Fprintf(&b, "build sr_g%d: phony %s\n", g, strings.Join(leaves, " "))
		sr = append(sr, fmt.Sprintf("sr_g%d", g))
	}
	fmt.Fprintf(&b, "build sysroot: phony %s\n", strings.Join(sr, " "))
	var gen []string
	for l := range 50 {
		gen = append(gen, fmt.Sprintf("../../gen/f%d.h", l))
	}
	fmt.Fprintf(&b, "build gen: phony %s\n", strings.Join(gen, " "))
	for i := range 4 {
		var srcs []string
		for j := range 6 {
			srcs = append(srcs, fmt.Sprintf("../../src/c%d/f%d.cc", i, j))
		}
		fmt.Fprintf(&b, "build obj/c%d.o: r %s toolchain sysroot\n", i, strings.Join(srcs, " "))
		srcs = srcs[:0]
		for j := range 6 {
			srcs = append(srcs, fmt.Sprintf("../../src/s%d/f%d.cc", i, j))
		}
		fmt.Fprintf(&b, "build obj/s%d.o: r %s gen\n", i, strings.Join(srcs, " "))
	}
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
		ins = append(ins, fmt.Sprintf("g%d", i))
	}
	fmt.Fprintf(&b, "build big: r %s\n", strings.Join(ins, " "))
	return b.String()
}

// BenchmarkSeenSet runs ExpandedTriggerInputs and DepsBaseInputs on
// sequences of steps per iteration:
//   - compile: one compile step with a 25k-leaf toolchain+sysroot.
//   - small: one step with a 50-leaf phony.
//   - mixed: 4 compile steps and 4 small steps, interleaved.
//   - mixedbig: mixed plus the 50k-input "big" step.
func BenchmarkSeenSet(b *testing.B) {
	graph, state := newTestGraph(b, b.TempDir(), compileManifest())
	ctx := b.Context()
	c := newTestStepDef(b, graph, state, "obj/c0.o")
	if n, want := len(c.ExpandedTriggerInputs(ctx)), 6+25000; n != want {
		b.Fatalf("ExpandedTriggerInputs(c0)=%d; want %d", n, want)
	}
	steps := map[string][]*StepDef{}
	for i := range 4 {
		cs := newTestStepDef(b, graph, state, fmt.Sprintf("obj/c%d.o", i))
		ss := newTestStepDef(b, graph, state, fmt.Sprintf("obj/s%d.o", i))
		if i == 0 {
			steps["compile"] = []*StepDef{cs}
			steps["small"] = []*StepDef{ss}
		}
		steps["mixed"] = append(steps["mixed"], cs, ss)
	}
	steps["mixedbig"] = append(append([]*StepDef(nil), steps["mixed"]...), newTestStepDef(b, graph, state, "big"))
	for _, fn := range []struct {
		name string
		f    func(*StepDef) []path.Path
	}{
		{"ExpandedTriggerInputs", func(s *StepDef) []path.Path { return s.ExpandedTriggerInputs(ctx) }},
		{"DepsBaseInputs", func(s *StepDef) []path.Path { return s.DepsBaseInputs(ctx, nil, false) }},
	} {
		for _, seq := range []string{"compile", "small", "mixed", "mixedbig"} {
			b.Run(fn.name+"/"+seq, func(b *testing.B) {
				// Start from the same pools in every sub-benchmark:
				// empty them, then leave the maps of a compile step.
				runtime.GC()
				runtime.GC()
				fn.f(steps["compile"][0])
				ss := steps[seq]
				for _, s := range ss {
					fn.f(s)
				}
				b.ReportAllocs()
				for b.Loop() {
					for _, s := range ss {
						fn.f(s)
					}
				}
			})
		}
	}
}

// rlibManifest has a Rust-like step "main" with one source and 100 rlib
// inputs, each built from 200 sources.
func rlibManifest() string {
	var b strings.Builder
	b.WriteString("rule r\n  command = x\n")
	var ins []string
	for i := range 100 {
		var srcs []string
		for j := range 200 {
			srcs = append(srcs, fmt.Sprintf("../../src/lib%d/f%d.rs", i, j))
		}
		fmt.Fprintf(&b, "build lib%d.rlib: r %s\n", i, strings.Join(srcs, " "))
		ins = append(ins, fmt.Sprintf("lib%d.rlib", i))
	}
	fmt.Fprintf(&b, "build main: r ../../src/main.rs %s\n", strings.Join(ins, " "))
	return b.String()
}

// BenchmarkExpandedInputs_indirect runs ExpandedInputs on "main" with
// indirect_inputs of *.rlib, so that its indirect inputs map grows from
// about 100 to about 20k entries.
func BenchmarkExpandedInputs_indirect(b *testing.B) {
	ctx := b.Context()
	graph, state := newTestGraph(b, b.TempDir(), rlibManifest())
	sc := &StepConfig{Rules: []*StepRule{{
		Name:           "rust",
		ActionName:     "^r$",
		IndirectInputs: &PathFilter{Includes: []string{"*.rlib"}},
	}}}
	if err := sc.Init(ctx); err != nil {
		b.Fatal(err)
	}
	graph.globals.stepConfig = sc
	s := newTestStepDef(b, graph, state, "main")
	if !s.rule.IndirectInputs.enabled() {
		b.Fatal("indirect_inputs not enabled")
	}
	s.ExpandedInputs(ctx)
	b.ReportAllocs()
	for b.Loop() {
		s.ExpandedInputs(ctx)
	}
}
