// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/google/subcommands"
	"kythe.io/kythe/go/platform/kzip"
)

type lsCmd struct{}

func (lsCmd) Name() string     { return "ls" }
func (lsCmd) Synopsis() string { return "Lists all compilation units in a kzip file." }
func (lsCmd) Usage() string {
	return `ls <kzip_path>
Lists all compilation units in a kzip file.
`
}
func (lsCmd) SetFlags(f *flag.FlagSet) {}

func (c lsCmd) Execute(ctx context.Context, f *flag.FlagSet, args ...interface{}) subcommands.ExitStatus {
	if f.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "Error: No kzip file was provided.\n\nUsage: %s\n", c.Usage())
		return subcommands.ExitUsageError
	}
	kzipPath := f.Arg(0)

	file, err := os.Open(kzipPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open kzip file %s: %v\n", kzipPath, err)
		return subcommands.ExitFailure
	}
	defer func() {
		if err := file.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to close %s: %v\n", kzipPath, err)
		}
	}()

	stat, err := file.Stat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to stat kzip file %s: %v\n", kzipPath, err)
		return subcommands.ExitFailure
	}

	fileSize := stat.Size()
	reader, err := kzip.NewReader(file, fileSize)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create kzip reader for %s: %v\n", kzipPath, err)
		return subcommands.ExitFailure
	}

	fmt.Printf("Compilation Units in %s:\n", kzipPath)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Digest\tLanguage\tPrimary Source\tInputs\tOutput Key")

	count := 0
	scanErr := reader.Scan(func(unit *kzip.Unit) error {
		count++
		lang := "unknown"
		primarySource := "n/a"
		outputKey := "n/a"

		reqInputs := 0
		if unit.Proto != nil {
			if vname := unit.Proto.GetVName(); vname != nil {
				lang = vname.GetLanguage()
			}
			if len(unit.Proto.GetSourceFile()) > 0 {
				primarySource = unit.Proto.GetSourceFile()[0]
			}
			outputKey = unit.Proto.GetOutputKey()
			reqInputs = len(unit.Proto.GetRequiredInput())
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", unit.Digest, lang, primarySource, reqInputs, outputKey)
		return nil
	})

	if scanErr != nil {
		fmt.Fprintf(os.Stderr, "Error scanning kzip: %v\n", scanErr)
		return subcommands.ExitFailure
	}
	err = tw.Flush()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error flushing: %v\n", err)
	}
	fmt.Printf("\nFound %d compilation units.\n", count)

	return subcommands.ExitSuccess
}
