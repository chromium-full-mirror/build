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
	"os"

	"github.com/google/subcommands"

	"go.chromium.org/build/gong/gn"
	"go.chromium.org/build/gong/gn/build"
	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/graph"
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
	setup := build.NewSetup()
	if err := setup.DoSetup(dir, true, &h.CommonFlags); err != nil {
		return err
	}

	toolchains := make(map[environment.Label]*graph.Toolchain)
	targetsByToolchain := make(map[environment.Label][]*graph.Target)
	for item, err := range setup.Items() {
		if err != nil {
			return fmt.Errorf("build failed: %w", err)
		}
		switch i := item.(type) {
		case *graph.Target:
			// TODO: write this target's subninja out.
			fmt.Fprintf(os.Stderr, "DEBUG: collected target %s\n", i.Label().UserVisibleString(true))
			// Bucket this target by toolchain, so that when we write toolchains we can reference the subninjas.
			tcLabel := i.Label().ToolchainLabel()
			targetsByToolchain[tcLabel] = append(targetsByToolchain[tcLabel], i)
		case *graph.Toolchain:
			toolchains[i.Label()] = i
		}
	}

	for tcLabel, targets := range targetsByToolchain {
		tc, ok := toolchains[tcLabel]
		if !ok {
			return fmt.Errorf("couldn't find toolchain %s", tcLabel.UserVisibleString(false))
		}
		// TODO: write this toolchain out.
		fmt.Fprintf(os.Stderr, "DEBUG: need to write out toolchain %s and its %d targets\n",
			tc.Label().UserVisibleString(false), len(targets))
	}

	return fmt.Errorf("genOneDir not implemented. setup: %v", setup)
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
