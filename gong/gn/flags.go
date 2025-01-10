// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package gn implements GN related functionality.
package gn

import (
	"github.com/maruel/subcommands"
)

// CommonFlags contains flags shared amongst all subcommands.
type CommonFlags struct {
	subcommands.CommandRunBase
	Args                string
	Color               bool
	Dotfile             string
	FailOnUnusedArgs    bool
	Markdown            bool
	NoColor             bool
	NinjaExecutable     string
	ScriptExecutable    string
	Quiet               bool
	Root                string
	RootTarget          string
	RootPattern         string
	RuntimeDepsListFile string
	Threads             int
	Time                bool
	Tracelog            string
}

// InitFlags initializes the common flags.
func (c *CommonFlags) InitFlags() {
	c.Flags.StringVar(&c.Args, "args", "", "Specifies build arguments overrides.")
	c.Flags.BoolVar(&c.Color, "color", false, "Force colored output.")
	c.Flags.StringVar(&c.Dotfile, "dotfile", "", "Override the name of the .gn file.")
	c.Flags.BoolVar(&c.FailOnUnusedArgs, "fail-on-unused-args", false, "Treat unused build args as fatal errors.")
	c.Flags.BoolVar(&c.Markdown, "markdown", false, "Write help output in the Markdown format.")
	c.Flags.BoolVar(&c.NoColor, "nocolor", false, "Force non-colored output.")
	c.Flags.StringVar(&c.NinjaExecutable, "ninja-executable", "", "Set the Ninja executable.")
	c.Flags.StringVar(&c.ScriptExecutable, "script-executable", "", "Set the executable used to execute scripts.")
	c.Flags.BoolVar(&c.Quiet, "q", false, "Quiet mode. Don't print output on success.")
	c.Flags.StringVar(&c.Root, "root", "", "Explicitly specify source root.")
	c.Flags.StringVar(&c.RootTarget, "root-target", "", "Override the root target.")
	c.Flags.StringVar(&c.RootPattern, "root-pattern", "", "Add root pattern override.")
	c.Flags.StringVar(&c.RuntimeDepsListFile, "runtime-deps-list-file", "", "Save runtime dependencies for targets in file.")
	c.Flags.IntVar(&c.Threads, "threads", 0, "Specify number of worker threads.")
	c.Flags.BoolVar(&c.Time, "time", false, "Outputs a summary of how long everything took.")
	c.Flags.StringVar(&c.Tracelog, "tracelog", "", "Writes a Chrome-compatible trace log to the given file.")
}
