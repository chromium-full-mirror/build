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

// LogoutCmd creates new LogoutCommand.
func LogoutCmd(authOpts cred.Options) *LogoutCommand {
	return &LogoutCommand{
		authOpts: authOpts,
	}
}

func (*LogoutCommand) Name() string {
	return "logout"
}

func (*LogoutCommand) Synopsis() string {
	return "logout from siso system"
}

func (*LogoutCommand) Usage() string {
	return "logout from siso system."
}

// LogoutCommand implements logout subcommand.
type LogoutCommand struct {
	authOpts cred.Options
}

func (*LogoutCommand) SetFlags(flagSet *flag.FlagSet) {}

func (c *LogoutCommand) Execute(ctx context.Context, flagSet *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	switch c.authOpts.Type {
	case "luci-auth":
		fmt.Println("using luci-auth for auth")
		cmd := exec.CommandContext(ctx, "luci-auth", "logout", "--scopes", "https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/cloud-platform")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return subcommands.ExitFailure
		}
		return subcommands.ExitSuccess

	case "gcloud":
		fmt.Println("using gcloud for auth")
		cmd := exec.CommandContext(ctx, "gcloud", "auth", "revoke")
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
