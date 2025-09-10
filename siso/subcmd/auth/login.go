// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package auth

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/google/subcommands"

	"go.chromium.org/build/siso/auth/cred"
)

// LoginCmd creates new LoginCommand.
func LoginCmd(authOpts cred.Options) *LoginCommand {
	return &LoginCommand{
		authOpts: authOpts,
	}
}

func (*LoginCommand) Name() string {
	return "login"
}

func (*LoginCommand) Synopsis() string {
	return "login to siso system"
}

func (*LoginCommand) Usage() string {
	return "login to siso system."
}

// LoginCommand implements login subcommand.
type LoginCommand struct {
	authOpts cred.Options
}

func (*LoginCommand) SetFlags(flagSet *flag.FlagSet) {}

func (c *LoginCommand) Execute(ctx context.Context, flagSet *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	switch c.authOpts.Type {
	case "luci-auth":
		fmt.Println("using luci-auth for auth")
		cmd := exec.CommandContext(ctx, "luci-auth", "login", "--scopes", "https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/cloud-platform")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			fmt.Fprintf(os.Stderr, "If you got 'This app is blocked', see https://chromium.googlesource.com/build/+/refs/heads/main/siso/docs/auth.md#this-app-is-blocked\n")
			return subcommands.ExitFailure
		}
		return subcommands.ExitSuccess

	case "gcloud":
		fmt.Println("using gcloud for auth")
		cmd := exec.CommandContext(ctx, "gcloud", "auth", "login", "--update-adc")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return subcommands.ExitFailure
		}
		return subcommands.ExitSuccess

	default:
		fmt.Printf("unsupported auth type for login: %s\n", c.authOpts.Type)
		return subcommands.ExitUsageError
	}
}
