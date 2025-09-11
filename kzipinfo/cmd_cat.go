// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/google/subcommands"
	"kythe.io/kythe/go/platform/kzip"
)

type catCmd struct{}

func (catCmd) Name() string     { return "cat" }
func (catCmd) Synopsis() string { return "Prints the content of a stored file by its digest." }
func (catCmd) Usage() string {
	return `cat <kzip_path> <file_digest>
Retrieves and prints the complete content of the file identified by the given digest.
`
}
func (catCmd) SetFlags(f *flag.FlagSet) {}

func (c catCmd) Execute(ctx context.Context, f *flag.FlagSet, args ...interface{}) subcommands.ExitStatus {
	if f.NArg() != 2 {
		fmt.Fprintf(os.Stderr, "Error: Both a kzip file and file digest must be provided.\n\nUsage: %s\n", c.Usage())
		return subcommands.ExitUsageError
	}
	kzipPath := f.Arg(0)
	fileDigest := f.Arg(1)

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

	rc, err := reader.Open(fileDigest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening file with digest %s: %v\n", fileDigest, err)
		return subcommands.ExitFailure
	}
	defer rc.Close()

	if _, err := io.Copy(os.Stdout, rc); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file content: %v\n", err)
		return subcommands.ExitFailure
	}

	return subcommands.ExitSuccess
}
