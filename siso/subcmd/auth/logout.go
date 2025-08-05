// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package auth

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/maruel/subcommands"

	"go.chromium.org/luci/common/cli"

	"go.chromium.org/build/siso/auth/cred"
)

func LogoutCmd(authOpts cred.Options) *subcommands.Command {
	return &subcommands.Command{
		UsageLine: "logout",
		ShortDesc: "logout from siso system",
		LongDesc:  "Logout from siso system.",
		CommandRun: func() subcommands.CommandRun {
			r := &logoutRun{authOpts: authOpts}
			r.init()
			return r
		},
	}
}

type logoutRun struct {
	subcommands.CommandRunBase
	authOpts cred.Options
}

func (r *logoutRun) init() {
}

func (r *logoutRun) Run(a subcommands.Application, args []string, env subcommands.Env) int {
	ctx := cli.GetContext(a, r, env)
	switch r.authOpts.Type {
	case "luci-auth":
		fmt.Println("using luci-auth for auth")
		cmd := exec.CommandContext(ctx, "luci-auth", "logout", "--scopes", "https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/cloud-platform")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return 1
		}
		return 0

	case "gcloud":
		fmt.Println("using gcloud for auth")
		cmd := exec.CommandContext(ctx, "gcloud", "auth", "revoke")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return 1
		}
		return 0

	default:
		fmt.Printf("unsupported auth type for login: %s\n", r.authOpts.Type)
	}
	return 0
}
