// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package sandbox is sandbox subcommand for debugging sandbox.
package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/google/subcommands"

	"go.chromium.org/build/siso/toolsupport/nsjailutil"
)

const usage = `run in sandbox

 $ siso sandbox -nsjail '<json nsjail request>' -- args ...

You can manually construct json string of
go.chromium.org/build/siso/toolsupport/nsjail.Request.
`

// Cmd returns the Command for the `sandbox` subcommand provided by this package.
func Cmd() *Command {
	return &Command{}
}

func (*Command) Name() string {
	return "sandbox"
}

func (*Command) Synopsis() string {
	return "run in sandbox"
}

func (*Command) Usage() string {
	return usage
}

// Command implements sandbox subcommand.
type Command struct {
	nsjailReqJSONString string
	cleanup             bool
	cmdline             []string
}

func (c *Command) SetFlags(flagSet *flag.FlagSet) {
	flagSet.StringVar(&c.nsjailReqJSONString, "nsjail", "", "json format of nsjail request")
	flagSet.BoolVar(&c.cleanup, "cleanup", true, "cleanup sandbox after execution")

}

func (c *Command) Execute(ctx context.Context, flagSet *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	c.cmdline = flagSet.Args()
	err := c.run(ctx)
	if err != nil {
		switch {
		case errors.Is(err, flag.ErrHelp):
			fmt.Fprintf(os.Stderr, "%v\n%s", err, usage)
			return subcommands.ExitUsageError
		default:
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return subcommands.ExitFailure
		}
	}
	return subcommands.ExitSuccess
}

func (c *Command) run(ctx context.Context) error {
	var req nsjailutil.Request
	if c.nsjailReqJSONString == "" {
		return fmt.Errorf("no nsjail request")
	}
	err := json.Unmarshal([]byte(c.nsjailReqJSONString), &req)
	if err != nil {
		return err
	}
	fsys := os.DirFS("/") // TODO: use hashfs?

	jail, err := nsjailutil.New(ctx, fsys, req)
	if err != nil {
		return err
	}
	if c.cleanup {
		defer jail.Close()
	}

	result, err := jail.Run(ctx, c.cmdline...)
	fmt.Printf("exit=%d\n", result.ExitCode)
	fmt.Printf("stdout:\n%s\n", result.Stdout)
	fmt.Printf("stderr:\n%s\n", result.Stderr)
	return err
}
