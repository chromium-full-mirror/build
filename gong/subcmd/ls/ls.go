// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package ls provides ls subcommand.
package ls

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"

	"github.com/google/subcommands"

	"go.chromium.org/build/gong/gn"
	"go.chromium.org/build/gong/gn/build"
	"go.chromium.org/build/gong/gn/build/graph"
	"go.chromium.org/build/gong/ui"
)

// Command implements ls subcommand.
type Command struct {
	gn.CommonFlags
}

func (*Command) Name() string     { return "ls" }
func (*Command) Synopsis() string { return "List matching targets." }
func (*Command) Usage() string {
	return `gn ls <out_dir>

  Lists all targets matching the given pattern for the given build directory.

  Support for toolchains other than the default is not yet implemented.`
}
func (h *Command) SetFlags(f *flag.FlagSet) {
	h.SetCommonFlags(f)
}

func (h *Command) getTargets(dir string) ([]*graph.Target, error) {
	setup := build.NewSetup()
	if err := setup.DoSetup(dir, true, &h.CommonFlags); err != nil {
		return nil, err
	}
	targets, err := setup.Run()
	if err != nil {
		return nil, err
	}
	slices.SortFunc(targets, func(a, b *graph.Target) int {
		return a.Label().Compare(b.Label())
	})
	return targets, nil
}

func (h *Command) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	dir := f.Arg(0)
	if dir == "" {
		fmt.Fprintf(os.Stderr, "expected build dir, got none\n")
		return subcommands.ExitFailure
	}

	if f.Arg(1) != "" {
		fmt.Fprintf(os.Stderr, "<label_pattern> is not yet supported\n")
		return subcommands.ExitFailure
	}

	targets, err := h.getTargets(dir)
	if err != nil {
		if e, ok := errors.AsType[ui.PresentableError](err); ok {
			fmt.Fprint(os.Stderr, ui.FormatError(e))
			return subcommands.ExitFailure
		}
		fmt.Fprintf(os.Stderr, "setup failed with error: %v\n", err)
		return subcommands.ExitFailure
	}

	// Placeholder implementation that only handles default toolchain.
	for _, target := range targets {
		fmt.Println(target.Label().UserVisibleString(false))
	}

	return subcommands.ExitSuccess
}
