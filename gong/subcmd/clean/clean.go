// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package clean provides clean subcommand.
package clean

import (
	"errors"
	"fmt"
	"os"

	"github.com/maruel/subcommands"

	"go.chromium.org/build/gong/gn"
	"go.chromium.org/build/gong/gn/syntax"
	"go.chromium.org/build/gong/ui"
)

// Cmd returns the Command for the `clean` subcommand provided by this package.
func Cmd() *subcommands.Command {
	return &subcommands.Command{
		UsageLine: "clean <out_dir>...",
		ShortDesc: "cleans the output directory",
		LongDesc:  "Deletes the contents of the output directory except for args.gn and creates a Ninja build environment sufficient to regenerate the build.",
		CommandRun: func() subcommands.CommandRun {
			ret := &cleanCmdRun{}
			ret.InitFlags()
			return ret
		},
	}
}

type cleanCmdRun struct {
	gn.CommonFlags
}

func (h *cleanCmdRun) cleanOneDir(dir string) error {
	setup := gn.NewSetup()
	if err := setup.DoSetup(dir, false, &h.CommonFlags); err != nil {
		return err
	}
	return fmt.Errorf("not implemented. setup: %v", setup)
}

func (h *cleanCmdRun) Run(a subcommands.Application, args []string, env subcommands.Env) int {
	for _, dir := range args {
		if err := h.cleanOneDir(dir); err != nil {
			var syntaxErr syntax.Error
			if errors.As(err, &syntaxErr) {
				fmt.Fprint(os.Stderr, ui.FormatError(syntaxErr))
				return 1
			}
			fmt.Fprintf(os.Stderr, "clean failed with error: %v\n", err)
			return 1
		}
	}
	return 0
}
