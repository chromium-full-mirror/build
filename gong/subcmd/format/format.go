// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package format provides format subcommand.
package format

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/google/subcommands"

	"go.chromium.org/build/gong/gn"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

// Command implements format subcommand.
type Command struct {
	gn.CommonFlags
	format string
}

func (*Command) Name() string     { return "format" }
func (*Command) Synopsis() string { return "formatted output of .gn files" }
func (*Command) Usage() string {
	return `subset of the gn format command that only supports --dump-tree for one file.

 $ gong format --dump-tree <format> <build_file>

format: text or json`
}
func (h *Command) SetFlags(f *flag.FlagSet) {
	h.SetCommonFlags(f)
	f.StringVar(&h.format, "dump-tree", "", `output format. "text" or "json"`)
}

func (h *Command) Execute(ctx context.Context, f *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	buildFile := f.Arg(0)
	if buildFile == "" {
		fmt.Fprintf(os.Stderr, "expected build file, got none\n")
		return subcommands.ExitFailure
	}

	if h.format != "text" && h.format != "json" {
		fmt.Fprintf(os.Stderr, "--dump-tree must be one of 'text' or 'json'\n")
		return subcommands.ExitFailure
	}

	dump, err := h.dumpTree(buildFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "format failed with error: %v\n", err)
		return subcommands.ExitFailure
	}
	fmt.Print(dump, "\n")
	return subcommands.ExitSuccess
}

func (h *Command) dumpTree(buildFile string) (string, error) {
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
