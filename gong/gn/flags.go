// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package gn implements GN related functionality.
package gn

import (
	"flag"
)

// CommonFlags contains flags shared amongst all subcommands.
type CommonFlags struct {
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

// SetCommonFlags initializes the common flags.
func (c *CommonFlags) SetCommonFlags(f *flag.FlagSet) {
	// The below are from C++ GN and do not follow the Go Style Decisions re flag naming
	// (https://google.github.io/styleguide/go/decisions.html#flags) for compatibility.
	f.StringVar(&c.Args, "args", "", "Specifies build arguments overrides.")
	f.BoolVar(&c.Color, "color", false, "Force colored output.")
	f.StringVar(&c.Dotfile, "dotfile", "", "Override the name of the .gn file.")
	f.BoolVar(&c.FailOnUnusedArgs, "fail-on-unused-args", false, "Treat unused build args as fatal errors.")
	f.BoolVar(&c.Markdown, "markdown", false, "Write help output in the Markdown format.")
	f.BoolVar(&c.NoColor, "nocolor", false, "Force non-colored output.")
	f.StringVar(&c.NinjaExecutable, "ninja-executable", "", "Set the Ninja executable.")
	f.StringVar(&c.ScriptExecutable, "script-executable", "", "Set the executable used to execute scripts.")
	f.BoolVar(&c.Quiet, "q", false, "Quiet mode. Don't print output on success.")
	f.StringVar(&c.Root, "root", "", "Explicitly specify source root.")
	f.StringVar(&c.RootTarget, "root-target", "", "Override the root target.")
	f.StringVar(&c.RootPattern, "root-pattern", "", "Add root pattern override.")
	f.StringVar(&c.RuntimeDepsListFile, "runtime-deps-list-file", "", "Save runtime dependencies for targets in file.")
	f.IntVar(&c.Threads, "threads", 0, "Specify number of worker threads.")
	f.BoolVar(&c.Time, "time", false, "Outputs a summary of how long everything took.")
	f.StringVar(&c.Tracelog, "tracelog", "", "Writes a Chrome-compatible trace log to the given file.")
}
