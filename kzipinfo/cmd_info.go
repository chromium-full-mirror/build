// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/maruel/subcommands"
	"kythe.io/kythe/go/platform/kzip/info"
)

type infoCmd struct {
	subcommands.CommandRunBase
}

func (c *infoCmd) Run(a subcommands.Application, args []string, env subcommands.Env) int {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "%s %s: expected <kzip_path>\n", a.GetName(), cmdInfo.Name())
		return 1
	}
	kzipPath := args[0]

	file, err := os.Open(kzipPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening kzip file: %v\n", err)
		return 1
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error stating kzip file: %v\n", err)
		return 1
	}

	info, err := info.KzipInfo(file, stat.Size())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting kzip info: %v\n", err)
		return 1
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

	return 0
}

var cmdInfo = &subcommands.Command{
	UsageLine: "info <kzip_path>",
	ShortDesc: "Shows summary information of a kzip file.",
	LongDesc:  "Displays summary information about the kzip file, including total CUs, file counts per corpus/language, size, and any critical errors.",
	CommandRun: func() subcommands.CommandRun {
		return new(infoCmd)
	},
}
