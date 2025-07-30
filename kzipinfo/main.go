// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Binary kzipinfo is a tool to help explore kzip files in a human-readable format.
package main

import (
	"os"

	"github.com/maruel/subcommands"
)

var application = &subcommands.DefaultApplication{
	Name:  "kzipinfo",
	Title: "A tool to inspect Kythe kzip files in a human-readable format.",
	Commands: []*subcommands.Command{
		cmdInfo,
		cmdListUnits,
		cmdShow,
		subcommands.CmdHelp,
	},
}

func main() {
	os.Exit(subcommands.Run(application, nil))
}
