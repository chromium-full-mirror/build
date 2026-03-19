// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package help provides help subcommand.
package help

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/google/subcommands"

	"go.chromium.org/build/gong/gn/build/analysis"
	"go.chromium.org/build/gong/gn/build/ninjawriter"
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

		fmt.Fprintf(os.Stdout, "\nBuildfile functions ")
		fmt.Fprintf(os.Stdout, `(type "%s help <function>" for more help)`, subcommands.DefaultCommander.Name())
		fmt.Fprintf(os.Stdout, ":\n")
		for _, name := range slices.Sorted(maps.Keys(analysis.FunctionMap)) {
			fmt.Fprintf(os.Stdout, "  %s\n", analysis.FunctionMap[name].HelpShort())
		}

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

	case "file_pattern":
		fmt.Fprint(os.Stdout, analysis.PatternsHelp)
		return subcommands.ExitSuccess

	case "ninja_rules":
		fmt.Fprint(os.Stdout, ninjawriter.NinjaRulesHelp)
		return subcommands.ExitSuccess

	default:
		// First try to find it as a subcommand.
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

		// Then try to find it as a build function.
		if info, ok := analysis.FunctionMap[what]; ok {
			fmt.Fprintf(os.Stdout, "%s\n%s", info.HelpShort(), info.Help())
			return subcommands.ExitSuccess
		}

		// Otherwise, print a GN style error first.
		fmt.Fprint(os.Stdout, ui.FormatError(err{fmt.Sprintf("No help on %q.", what)}))

		// Then print help text for what to do.
		fmt.Fprintf(os.Stdout, "Run `%s help` for a list of available topics.\n", subcommands.DefaultCommander.Name())
		return subcommands.ExitUsageError
	}
}

type err struct {
	message string
}

// Error implements PresentableError.
func (e err) Error() string { return fmt.Sprintf("help error: %s", e.message) }

// Message implements PresentableError.
func (e err) Message() string { return e.message }

// HelpText implements PresentableError.
func (e err) HelpText() string { return "" }
