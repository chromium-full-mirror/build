// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package gen provides gen subcommand.
package gen

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"time"

	"github.com/google/subcommands"

	"go.chromium.org/build/gong/gn"
	"go.chromium.org/build/gong/gn/build"
	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/graph"
	"go.chromium.org/build/gong/gn/build/ninjawriter"
	"go.chromium.org/build/gong/ui"
)

// Command implements gen subcommand.
type Command struct {
	gn.CommonFlags
}

func (*Command) Name() string     { return "gen" }
func (*Command) Synopsis() string { return "Generate Ninja files." }
func (*Command) Usage() string {
	return `gn gen <out_dir>

  Generates ninja files from the current tree and puts them in the given output
  directory.

  Not implemented yet, won't actually work.`
}
func (h *Command) SetFlags(f *flag.FlagSet) {
	h.SetCommonFlags(f)
}

func (h *Command) genOneDir(dir string) error {
	start := time.Now()

	setup := build.NewSetup()
	if err := setup.DoSetup(dir, true, &h.CommonFlags); err != nil {
		return err
	}

	toolchains := make(map[environment.Label]*graph.Toolchain)
	targetsByToolchain := make(map[environment.Label][]*graph.Target)
	// TODO: Because this is an iterator over the build as it progresses,
	// we may be able to perform the same C++ GN optimization of async writing
	// each target as the build progresses. This would look a bit different though,
	// because unlike C++ GN we would use goroutines not callbacks.
	//
	// For example:
	// - Writing targets
	//   - Start errgroup.Group for each item to call ninjawriter.WriteTarget
	// - Writing toolchain
	//   - Have chan for each toolchain that receives graph.Item
	//     then it can write the toolchain.ninja and ninja rules for each item
	// Or some combination, e.g.
	// - Should targets be written in a loop separate from toolchains like C++ GN?
	// - Should targets be written by the toolchain chan?
	//
	// Regardless, implementing the above now is premature optimization.
	// We don't know how much speed optimization we'll get out, especially because
	// our async model is very different to C++ GN.
	//
	// So right now we'll just implement sync generation model.
	for item, err := range setup.Items() {
		if err != nil {
			return fmt.Errorf("build failed: %w", err)
		}
		switch i := item.(type) {
		case *graph.Target:
			tcLabel := i.Label().ToolchainLabel()
			targetsByToolchain[tcLabel] = append(targetsByToolchain[tcLabel], i)
		case *graph.Toolchain:
			toolchains[i.Label()] = i
		}
	}

	if err := ninjawriter.Write(toolchains, targetsByToolchain, &setup.BuildSettings); err != nil {
		return fmt.Errorf("write ninja files failed: %w", err)
	}

	elapsed := time.Since(start)
	targetsCollected := 0
	for ninjaRules := range maps.Values(targetsByToolchain) {
		targetsCollected += len(ninjaRules)
	}

	// TODO: get count of loaded files from fs.InputFileManager somehow?
	// TODO: color?
	fmt.Printf("Done. Made %d targets from ?? files in %dms\n", targetsCollected, elapsed.Milliseconds())
	return nil
}

func (h *Command) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	for _, dir := range f.Args() {
		if err := h.genOneDir(dir); err != nil {
			if e, ok := errors.AsType[ui.PresentableError](err); ok {
				fmt.Fprint(os.Stderr, ui.FormatError(e))
				return subcommands.ExitFailure
			}
			fmt.Fprintf(os.Stderr, "gen failed with error: %v\n", err)
			return subcommands.ExitFailure
		}
	}
	return subcommands.ExitSuccess
}
