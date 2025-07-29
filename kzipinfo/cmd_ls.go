// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/maruel/subcommands"
	"kythe.io/kythe/go/platform/kzip"
)

type lsCmd struct {
	subcommands.CommandRunBase
}

func (c *lsCmd) Run(a subcommands.Application, args []string, env subcommands.Env) int {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "%s %s: expected <kzip_path>\n", a.GetName(), cmdListUnits.Name())
		return 1
	}
	kzipPath := args[0]

	file, err := os.Open(kzipPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open kzip file %s: %v\n", kzipPath, err)
		return 1
	}
	defer func() {
		if err := file.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to close %s: %v\n", kzipPath, err)
		}
	}()

	stat, err := file.Stat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to stat kzip file %s: %v\n", kzipPath, err)
		return 1
	}

	fileSize := stat.Size()
	reader, err := kzip.NewReader(file, fileSize)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create kzip reader for %s: %v\n", kzipPath, err)
		return 1
	}

	fmt.Printf("Compilation Units in %s:\n", kzipPath)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Digest\tLanguage\tPrimary Source\tOutput Key")

	count := 0
	scanErr := reader.Scan(func(unit *kzip.Unit) error {
		count++
		lang := "unknown"
		primarySource := "n/a"
		outputKey := "n/a"

		if unit.Proto != nil {
			if vname := unit.Proto.GetVName(); vname != nil {
				lang = vname.GetLanguage()
			}
			if len(unit.Proto.GetSourceFile()) > 0 {
				primarySource = unit.Proto.GetSourceFile()[0]
			}
			outputKey = unit.Proto.GetOutputKey()
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", unit.Digest, lang, primarySource, outputKey)
		return nil
	})

	if scanErr != nil {
		fmt.Fprintf(os.Stderr, "Error scanning kzip: %v\n", scanErr)
		return 1
	}
	err = tw.Flush()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error flushing: %v\n", err)
	}
	fmt.Printf("\nFound %d compilation units.\n", count)

	return 0
}

var cmdListUnits = &subcommands.Command{
	UsageLine: "ls <kzip_path>",
	ShortDesc: "Lists all compilation units in a kzip file.",
	LongDesc:  "Lists the digest, language, primary source, and output key for each compilation unit.",
	CommandRun: func() subcommands.CommandRun {
		return new(lsCmd)
	},
}
