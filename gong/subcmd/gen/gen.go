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
	if err := setup.Run(); err != nil {
		return fmt.Errorf("setup.Run failed: %w", err)
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
