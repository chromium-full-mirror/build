// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/google/subcommands"
	"kythe.io/kythe/go/platform/kzip/info"
)

type infoCmd struct{}

func (infoCmd) Name() string     { return "info" }
func (infoCmd) Synopsis() string { return "Shows summary information of a kzip file." }
func (infoCmd) Usage() string {
	return `info <kzip_path>
Shows summary information of a kzip file.
`
}
func (infoCmd) SetFlags(f *flag.FlagSet) {}

func (c infoCmd) Execute(ctx context.Context, f *flag.FlagSet, args ...interface{}) subcommands.ExitStatus {
	if f.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "Error: No kzip file was provided.\n\nUsage: %s\n", c.Usage())
		return subcommands.ExitUsageError
	}
	kzipPath := f.Arg(0)

	file, err := os.Open(kzipPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening kzip file: %v\n", err)
		return subcommands.ExitFailure
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error stating kzip file: %v\n", err)
		return subcommands.ExitFailure
	}

	info, err := info.KzipInfo(file, stat.Size())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting kzip info: %v\n", err)
		return subcommands.ExitFailure
	}

	fmt.Printf("Kzip Info for: %s\n", kzipPath)
	fmt.Printf("Size: %d bytes\n", info.GetSize())

	if len(info.GetCriticalKzipErrors()) > 0 {
		fmt.Println("Critical Kzip Errors:")
		for _, kerr := range info.GetCriticalKzipErrors() {
			fmt.Printf("  - %s\n", kerr)
		}
	}
	if len(info.GetAbsolutePaths()) > 0 {
		fmt.Printf("Absolute Paths Found:")
		for _, ap := range info.GetAbsolutePaths() {
			fmt.Printf("  - %s\n", ap)
		}
	}

	fmt.Println("Corpora Breakdown:")

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Corpus\tLanguage\tCU Count\tSource Files\tRequired Inputs")

	var totalCUs int32
	var totalSources int32
	var totalInputs int32
	corpora := info.GetCorpora()
	for _, corpusName := range slices.Sorted(maps.Keys(corpora)) {
		corpusInfo := corpora[corpusName]
		if corpusInfo == nil {
			continue
		}

		cuInfos := corpusInfo.GetLanguageCuInfo()
		for _, lang := range slices.Sorted(maps.Keys(cuInfos)) {
			cuInfo := cuInfos[lang]
			if cuInfo == nil {
				continue
			}
			sources := corpusInfo.GetLanguageSources()[lang]
			inputs := corpusInfo.GetLanguageRequiredInputs()[lang]

			var sourceCount int32
			if sources != nil {
				sourceCount = sources.GetCount()
			}
			var inputCount int32
			if inputs != nil {
				inputCount = inputs.GetCount()
			}

			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\n",
				corpusName, lang, cuInfo.GetCount(), sourceCount, inputCount)
			totalCUs += cuInfo.GetCount()
			totalSources += sourceCount
			totalInputs += inputCount
		}
	}
	fmt.Fprintln(tw, "----\t----\t----\t----\t----")
	fmt.Fprintf(tw, "Total\t\t%d\t%d\t%d\n", totalCUs, totalSources, totalInputs)
	tw.Flush()

	return subcommands.ExitSuccess
}
