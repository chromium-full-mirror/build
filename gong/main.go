// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// gong is an experimental Go implementation of the GN meta-build system.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"

	log "github.com/golang/glog"
	"github.com/google/subcommands"

	"go.chromium.org/build/gong/subcmd/clean"
	"go.chromium.org/build/gong/subcmd/format"
)

func main() {
	// Wraps gongMain() because os.Exit() doesn't wait defers.
	os.Exit(gongMain())
}

func gongMain() int {
	flag.CommandLine.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `
Usage: gong [command] [arguments]

Use "gong help" to display commands.
Use "gong help [command]" for more information about a command.

`)
		fmt.Fprintf(flag.CommandLine.Output(), "flags of %s:\n", os.Args[0])
		flag.PrintDefaults()
	}

	flag.Parse()

	// Flush the log on exit to not lose any messages.
	defer log.Flush()

	// Print a stack trace when a panic occurs.
	defer func() {
		if r := recover(); r != nil {
			const size = 64 << 10
			buf := make([]byte, size)
			buf = buf[:runtime.Stack(buf, false)]
			log.Fatalf("panic: %v\n%s", r, buf)
		}
	}()

	subcommands.Register(&clean.Command{}, "")
	subcommands.Register(&format.Command{}, "")
	subcommands.Register(subcommands.FlagsCommand(), "")
	subcommands.Register(subcommands.HelpCommand(), "")
	return int(subcommands.Execute(context.Background()))
}
