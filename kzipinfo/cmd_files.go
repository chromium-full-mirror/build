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
	"strings"
	"text/tabwriter"

	"github.com/google/subcommands"
	"kythe.io/kythe/go/platform/kzip"
)

type filesCmd struct{}

func (filesCmd) Name() string { return "files" }
func (filesCmd) Synopsis() string {
	return "Lists all unique files (by digest) and their known paths from all compilation units."
}
func (filesCmd) Usage() string {
	return `files <kzip_path>
Scans all compilation units, extracts required input file information, and lists unique file digests along with all paths they are referenced by.
`
}
func (filesCmd) SetFlags(f *flag.FlagSet) {}

func (c filesCmd) Execute(ctx context.Context, f *flag.FlagSet, args ...interface{}) subcommands.ExitStatus {
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

	// map[digest] -> map[path] -> struct{} (to store unique paths per digest)
	allFileInfos := make(map[string]map[string]struct{})

	// Scan all CUs -> scan all files in each CU.
	scanErr := reader.Scan(func(unit *kzip.Unit) error {
		if unit.Proto != nil {
			for _, reqInput := range unit.Proto.GetRequiredInput() {
				if info := reqInput.GetInfo(); info != nil {
					digest := info.GetDigest()
					path := info.GetPath()

					if _, ok := allFileInfos[digest]; !ok {
						allFileInfos[digest] = make(map[string]struct{})
					}
					allFileInfos[digest][path] = struct{}{}
				}
			}
		}
		return nil
	})

	if scanErr != nil {
		fmt.Fprintf(os.Stderr, "Error scanning kzip: %v\n", scanErr)
		return subcommands.ExitFailure
	}

	fmt.Printf("Unique Files in %s (by digest and paths):\n", kzipPath)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "File Digest\tKnown Paths")

	// Order by file digests.
	for _, digest := range slices.Sorted(maps.Keys(allFileInfos)) {
		pathsMap := allFileInfos[digest]
		// Then order by file paths.
		paths := slices.Sorted(maps.Keys(pathsMap))
		fmt.Fprintf(tw, "%s\t%s\n", digest, strings.Join(paths, ", "))
	}
	tw.Flush()
	fmt.Printf("\nFound %d unique file digests.\n", len(allFileInfos))

	return subcommands.ExitSuccess
}
