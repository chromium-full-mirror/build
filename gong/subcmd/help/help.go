// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package help provides help subcommand.
package help

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/google/subcommands"

	"go.chromium.org/build/gong/gn/syntax"
	"go.chromium.org/build/gong/ui"
)

// Command implements help subcommand.
type Command struct{}

func (*Command) Name() string           { return "help" }
func (*Command) Synopsis() string       { return "does what you think" }
func (*Command) SetFlags(*flag.FlagSet) {}
func (*Command) Usage() string {
	return `help <anything>:

  Yo dawg, I heard you like help on your help so I put help on the help in the
  help.

  You can also use "all" as the parameter to get all help at once.
`
}

func (h *Command) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	if f.NArg() == 0 {
		subcommands.DefaultCommander.Explain(os.Stdout)
		return subcommands.ExitSuccess
	}

	what := f.Arg(0)
	switch what {
	case "all":
		subcommands.DefaultCommander.VisitCommands(func(_ *subcommands.CommandGroup, c subcommands.Command) {
			subcommands.DefaultCommander.ExplainCommand(os.Stdout, c)
			fmt.Fprintf(os.Stdout, "\n")
		})
		return subcommands.ExitSuccess

	default:
		var command subcommands.Command
		subcommands.DefaultCommander.VisitCommands(func(_ *subcommands.CommandGroup, c subcommands.Command) {
			if c.Name() == what {
				command = c
			}
		})
		if command != nil {
			subcommands.DefaultCommander.ExplainCommand(os.Stdout, command)
			return subcommands.ExitSuccess
		}

		// Print as if it was a GN-style error.
		err := syntax.MakeErrorAt(syntax.Location{}, []syntax.LocationRange{},
			syntax.ErrUnknown,
			fmt.Sprintf("No help on %q.", what), "")
		var gnErr syntax.Error
		if errors.As(err, &gnErr) {
			fmt.Fprint(os.Stdout, ui.FormatError(gnErr))
		} else {
			// Just in case we messed up, fallback to Go style error printing.
			fmt.Fprint(os.Stdout, err)
		}
		fmt.Fprintf(os.Stdout, "Run `%s help` for a list of available topics.\n", subcommands.DefaultCommander.Name())
		return subcommands.ExitUsageError
	}
}
