// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package auth

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/maruel/subcommands"

	"go.chromium.org/build/siso/auth/cred"
)

func LoginCmd(authOpts cred.Options) *subcommands.Command {
	return &subcommands.Command{
		UsageLine: "login",
		ShortDesc: "login to siso system",
		LongDesc:  "Login to siso system.",
		CommandRun: func() subcommands.CommandRun {
			r := &loginRun{authOpts: authOpts}
			r.init()
			return r
		},
	}
}

type loginRun struct {
	subcommands.CommandRunBase
	authOpts cred.Options
}

func (r *loginRun) init() {
}

func (r *loginRun) Run(a subcommands.Application, args []string, env subcommands.Env) int {
	ctx := context.Background()
	switch r.authOpts.Type {
	case "luci-auth":
		fmt.Println("using luci-auth for auth")
		cmd := exec.CommandContext(ctx, "luci-auth", "login", "--scopes", "https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/cloud-platform")
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
		cmd := exec.CommandContext(ctx, "gcloud", "auth", "login", "--update-adc")
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
