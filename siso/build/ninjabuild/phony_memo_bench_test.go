// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjabuild

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/toolsupport/ninjautil"
)

type gnBenchSpec struct {
	name           string
	levels         int
	perLevel       int
	ownLeaves      int
	sharedHdrs     int
	numSteps       int
	stepGroups     int
	includeSDKStep bool
}

var (
	// Single umbrella target per step (like a GN .inputdeps.phony) with
	// 60 nested levels, shared common headers across component phonies,
	// ~25.1k unique leaves, 223 phony edges, ~4.1M flattened paths.
	gnSingleRootSpec = gnBenchSpec{
		name:           "single_root",
		levels:         60,
		perLevel:       3,
		ownLeaves:      25,
		sharedHdrs:     40,
		numSteps:       128,
		stepGroups:     1,
		includeSDKStep: false,
	}
	// Multi-group step (toolchain + sysroot + 8 overlapping component groups),
	// ~25.1k unique leaves, 60 nested levels, 223 phony edges, ~4.1M flattened paths.
	gnSharedSpec = gnBenchSpec{
		name:           "multi_group",
		levels:         60,
		perLevel:       3,
		ownLeaves:      25,
		sharedHdrs:     40,
		numSteps:       128,
		stepGroups:     8,
		includeSDKStep: true,
	}
	// Larger 493-phony graph (~11.2M flattened paths),
	// ~29.6k unique leaves, 75 nested levels.
	gnLargeSpec = gnBenchSpec{
		name:           "large",
		levels:         75,
		perLevel:       6,
		ownLeaves:      20,
		sharedHdrs:     30,
		numSteps:       128,
		stepGroups:     8,
		includeSDKStep: true,
	}
)

var allGNBenchSpecs = []gnBenchSpec{gnSingleRootSpec, gnSharedSpec, gnLargeSpec}

// buildGNBenchManifest builds a Chromium/GN-like phony graph in out/Default
// with 65-80 byte workspace-relative paths:
//   - 8,000 toolchain files across 16 sub-phonies
//   - 12,000 sysroot files across 24 sub-phonies
//   - levels * perLevel nested component phony groups in a diamond DAG,
//     sharing common headers across components
//   - numSteps compile steps
//   - probe steps touching every phony edge as a direct trigger input so
//     retained-memory tests populate 100% of phony edges.
func buildGNBenchManifest(spec gnBenchSpec) (manifest string, numProbes int) {
	var b strings.Builder
	b.Grow(6 << 20)
	b.WriteString("rule cxx\n  command = clang++ -c $in -o $out\n")

	var allPhonies []string
	var tcSubs []string
	for g := range 16 {
		var ins []string
		for f := range 500 {
			ins = append(ins, fmt.Sprintf("../../third_party/llvm-build/Release+Asserts/lib/clang/22/include/tc_g%02d_hdr_%04d.h", g, f))
		}
		name := fmt.Sprintf("obj/build/config/clang_sub_%02d.inputdeps.phony", g)
		fmt.Fprintf(&b, "build %s: phony %s\n", name, strings.Join(ins, " "))
		tcSubs = append(tcSubs, name)
		allPhonies = append(allPhonies, name)
	}
	const tcPhony = "obj/build/config/clang_toolchain_deps.inputdeps.phony"
	fmt.Fprintf(&b, "build %s: phony %s\n", tcPhony, strings.Join(tcSubs, " "))
	allPhonies = append(allPhonies, tcPhony)

	var sysSubs []string
	for g := range 24 {
		var ins []string
		for f := range 500 {
			ins = append(ins, fmt.Sprintf("../../build/linux/debian_bullseye_amd64-sysroot/usr/include/x86_64-linux-gnu/sys_%02d_%04d.h", g, f))
		}
		name := fmt.Sprintf("obj/build/linux/sysroot_sub_%02d.inputdeps.phony", g)
		fmt.Fprintf(&b, "build %s: phony %s\n", name, strings.Join(ins, " "))
		sysSubs = append(sysSubs, name)
		allPhonies = append(allPhonies, name)
	}
	const sysPhony = "obj/build/linux/debian_sysroot_deps.inputdeps.phony"
	fmt.Fprintf(&b, "build %s: phony %s\n", sysPhony, strings.Join(sysSubs, " "))
	allPhonies = append(allPhonies, sysPhony)

	const sdkPhony = "obj/build/config/sdk_and_toolchain.inputdeps.phony"
	fmt.Fprintf(&b, "build %s: phony %s %s\n", sdkPhony, tcPhony, sysPhony)
	allPhonies = append(allPhonies, sdkPhony)

	const commonPool = 500
	commonHdr := func(idx int) string {
		return fmt.Sprintf("../../base/allocator/partition_allocator/src/partition_alloc/common_%04d.h", idx%commonPool)
	}

	compName := func(lvl, k int) string {
		return fmt.Sprintf("obj/components/lvl_%02d/comp_%02d_inputdeps.phony", lvl, k)
	}
	for lvl := range spec.levels {
		for k := range spec.perLevel {
			var ins []string
			for f := range spec.ownLeaves {
				if f%2 == 0 {
					ins = append(ins, fmt.Sprintf("gen/third_party/blink/public/mojom/service_worker/l%02d_c%02d_f%03d.mojom-shared.h", lvl, k, f))
				} else {
					ins = append(ins, fmt.Sprintf("../../components/autofill/core/browser/payments/l%02d_c%02d_header_%03d.h", lvl, k, f))
				}
			}
			for h := range spec.sharedHdrs {
				ins = append(ins, commonHdr(lvl*13+k*7+h*3))
			}
			if lvl == 0 {
				ins = append(ins, sdkPhony)
			} else {
				ins = append(ins, compName(lvl-1, k%spec.perLevel))
				ins = append(ins, compName(lvl-1, (k+1)%spec.perLevel))
				if lvl >= 3 {
					ins = append(ins, compName(lvl-3, (k+2)%spec.perLevel))
				}
				if lvl%10 == 0 {
					ins = append(ins, tcPhony)
				}
			}
			name := compName(lvl, k)
			fmt.Fprintf(&b, "build %s: phony %s\n", name, strings.Join(ins, " "))
			allPhonies = append(allPhonies, name)
		}
	}

	for s := range spec.numSteps {
		var explicitIns []string
		explicitIns = append(explicitIns, fmt.Sprintf("../../chrome/browser/ui/views/feature_module_%03d/controller_%03d.cc", s%32, s))
		for h := range 3 {
			explicitIns = append(explicitIns, fmt.Sprintf("../../chrome/browser/ui/views/feature_module_%03d/local_hdr_%03d_%d.h", s%32, s, h))
		}
		var implicitIns []string
		if spec.includeSDKStep {
			implicitIns = append(implicitIns, tcPhony, sysPhony)
		}
		for g := range spec.stepGroups {
			lvl := spec.levels - 1 - ((s + g*5) % max(spec.levels/4, 1))
			k := (s + g) % spec.perLevel
			implicitIns = append(implicitIns, compName(lvl, k))
		}
		fmt.Fprintf(&b, "build obj/chrome/browser/unit_%03d.o: cxx %s | %s\n",
			s, strings.Join(explicitIns, " "), strings.Join(implicitIns, " "))
	}

	for i, ph := range allPhonies {
		fmt.Fprintf(&b, "build obj/probe/p_%04d.o: cxx | %s\n", i, ph)
	}
	return b.String(), len(allPhonies)
}

func setupGNBench(tb testing.TB, spec gnBenchSpec) (*Graph, *ninjautil.State, []*StepDef, int) {
	tb.Helper()
	manifest, numProbes := buildGNBenchManifest(spec)
	graph, state := newTestGraph(tb, tb.TempDir(), manifest)
	steps := make([]*StepDef, spec.numSteps)
	for i := range spec.numSteps {
		steps[i] = newTestStepDef(tb, graph, state, fmt.Sprintf("obj/chrome/browser/unit_%03d.o", i))
	}
	return graph, state, steps, numProbes
}

func forceFullGC() {
	seenSetPool = sync.Pool{
		New: func() any { return make(map[path.Path]bool, 1024) },
	}
	dedupSetPool = sync.Pool{
		New: func() any { return make(map[path.Path]bool, 1024) },
	}
	bigDedupSetPool = sync.Pool{}
	runtime.GC()
	runtime.GC()
	runtime.GC()
}

// BenchmarkPhonyMemoRetained reports the heap that the phony memo retains
// after every phony edge of the graph is expanded, in memo-MiB. Each
// iteration builds a new graph, so run it with -benchtime=1x.
func BenchmarkPhonyMemoRetained(b *testing.B) {
	for _, spec := range []gnBenchSpec{gnSharedSpec, gnLargeSpec} {
		b.Run(spec.name, func(b *testing.B) {
			var mib float64
			for range b.N {
				mib = measurePhonyMemoRetained(b, spec)
			}
			b.ReportMetric(mib, "memo-MiB")
		})
	}
}

func measurePhonyMemoRetained(b *testing.B, spec gnBenchSpec) float64 {
	ctx := b.Context()
	graph, state, steps, numProbes := setupGNBench(b, spec)
	probes := make([]*StepDef, numProbes)
	for i := range numProbes {
		probes[i] = newTestStepDef(b, graph, state, fmt.Sprintf("obj/probe/p_%04d.o", i))
	}
	for id := 1; id < state.NumNodes(); id++ {
		_, _ = graph.TargetPath(ctx, build.Target(id))
	}
	forceFullGC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for _, s := range append(probes, steps...) {
		_ = s.ExpandedTriggerInputs(ctx)
	}
	forceFullGC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(graph)
	runtime.KeepAlive(probes)
	return float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)) / (1 << 20)
}

func BenchmarkGNPhony_ExpandedTriggerInputs(b *testing.B) {
	for _, spec := range allGNBenchSpecs {
		b.Run(spec.name, func(b *testing.B) {
			ctx := b.Context()
			graph, state, steps, numProbes := setupGNBench(b, spec)
			for id := 1; id < state.NumNodes(); id++ {
				_, _ = graph.TargetPath(ctx, build.Target(id))
			}
			for i := range numProbes {
				ps := newTestStepDef(b, graph, state, fmt.Sprintf("obj/probe/p_%04d.o", i))
				_ = ps.ExpandedTriggerInputs(ctx)
			}
			for _, s := range steps {
				if n := len(s.ExpandedTriggerInputs(ctx)); n < 20000 {
					b.Fatalf("unexpected input count %d", n)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			i := 0
			for b.Loop() {
				_ = steps[i%len(steps)].ExpandedTriggerInputs(ctx)
				i++
			}
		})
	}
}

func BenchmarkGNPhony_DepsBaseInputs(b *testing.B) {
	for _, spec := range allGNBenchSpecs {
		b.Run(spec.name, func(b *testing.B) {
			ctx := b.Context()
			graph, state, steps, numProbes := setupGNBench(b, spec)
			for id := 1; id < state.NumNodes(); id++ {
				_, _ = graph.TargetPath(ctx, build.Target(id))
			}
			for i := range numProbes {
				ps := newTestStepDef(b, graph, state, fmt.Sprintf("obj/probe/p_%04d.o", i))
				_ = ps.ExpandedTriggerInputs(ctx)
			}
			for _, s := range steps {
				if n := len(s.DepsBaseInputs(ctx, nil, false)); n < 20000 {
					b.Fatalf("unexpected input count %d", n)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			i := 0
			for b.Loop() {
				_ = steps[i%len(steps)].DepsBaseInputs(ctx, nil, false)
				i++
			}
		})
	}
}
