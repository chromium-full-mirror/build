// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package clean provides clean subcommand.
package clean

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

// Command implements clean subcommand.
type Command struct {
	gn.CommonFlags
}

func (*Command) Name() string     { return "clean" }
func (*Command) Synopsis() string { return "cleans the output directory" }
func (*Command) Usage() string {
	return `clean <out_dir>...
Deletes the contents of the output directory except for args.gn and creates a Ninja build environment sufficient to regenerate the build.`
}
func (h *Command) SetFlags(f *flag.FlagSet) {
	h.SetCommonFlags(f)
}

func (h *Command) cleanOneDir(dir string) error { //nolint:staticcheck // SA4023: cleanOneDir is not implemented yet and always returns an error.
	setup := build.NewSetup()
	if err := setup.DoSetup(dir, false, &h.CommonFlags); err != nil {
		return err
	}
	return fmt.Errorf("not implemented. setup: %v", setup)
}

func (h *Command) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	for _, dir := range f.Args() {
		if err := h.cleanOneDir(dir); err != nil { //nolint:staticcheck // SA4023: cleanOneDir is not implemented yet and always returns an error.
			if e, ok := errors.AsType[ui.PresentableError](err); ok {
				fmt.Fprint(os.Stderr, ui.FormatError(e))
				return subcommands.ExitFailure
			}
			fmt.Fprintf(os.Stderr, "clean failed with error: %v\n", err)
			return subcommands.ExitFailure
		}
	}
	return subcommands.ExitSuccess
}
