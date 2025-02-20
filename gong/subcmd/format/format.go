// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package format provides format subcommand.
package format

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/maruel/subcommands"

	"go.chromium.org/build/gong/gn"
	"go.chromium.org/build/gong/gn/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

const formatUsage = `subset of the gn format command that only supports --dump-tree for one file.

 $ gong format --dump-tree <format> <build_file>

format: text or json
`

// Cmd returns the Command for the `format` subcommand provided by this package.
func Cmd() *subcommands.Command {
	return &subcommands.Command{
		UsageLine: "format --dump-tree <format> <build_file>",
		ShortDesc: "formatted output of .gn files",
		LongDesc:  formatUsage,
		CommandRun: func() subcommands.CommandRun {
			ret := &formatCmdRun{}
			ret.init()
			return ret
		},
	}
}

type formatCmdRun struct {
	gn.CommonFlags
	format string
}

func (h *formatCmdRun) init() {
	h.InitFlags()
	h.Flags.StringVar(&h.format, "dump-tree", "", `output format. "text" or "json"`)
}

func (h *formatCmdRun) Run(_ subcommands.Application, _ []string, _ subcommands.Env) int {
	buildFile := h.Flags.Arg(0)
	if buildFile == "" {
		fmt.Fprintf(os.Stderr, "expected build file, got none\n")
		return 1
	}

	if h.format != "text" && h.format != "json" {
		fmt.Fprintf(os.Stderr, "--dump-tree must be one of 'text' or 'json'\n")
		return 1
	}

	dump, err := h.dumpTree(buildFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "format failed with error: %v\n", err)
		return 1
	}
	fmt.Print(dump, "\n")
	return 0
}

func (h *formatCmdRun) dumpTree(buildFile string) (string, error) {
	inputFile, err := fs.NewInputFile("/BUILD.gn", buildFile)
	if err != nil {
		return "", fmt.Errorf("could not load as build file: %w", err)
	}

	tokens, err := syntax.Tokenize(inputFile)
	if err != nil {
		return "", fmt.Errorf("tokenize failed: %w", err)
	}

	root, err := parse.Parse(tokens)
	if err != nil {
		return "", fmt.Errorf("parse failed: %w", err)
	}

	if h.format == "text" {
		var buf strings.Builder
		err = parse.RenderDump(&buf, root.Dump())
		if err != nil {
			return "", fmt.Errorf("render failed: %w", err)
		}
		return buf.String(), nil
	}

	jsonData, err := json.MarshalIndent(root.Dump(), "", "  ")
	if err != nil {
		return "", fmt.Errorf("json marshal failed: %w", err)
	}
	return string(jsonData), nil
}
