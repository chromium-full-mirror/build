// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/maruel/subcommands"
	"kythe.io/kythe/go/platform/kzip"
	spb "kythe.io/kythe/proto/storage_go_proto"
)

// formatVName creates a string representation of a VName.
func formatVName(vname *spb.VName) string {
	if vname == nil {
		return "<nil>"
	}
	var parts []string
	if vname.GetSignature() != "" {
		parts = append(parts, "sig:"+vname.GetSignature())
	}
	if vname.GetCorpus() != "" {
		parts = append(parts, "corpus:"+vname.GetCorpus())
	}
	if vname.GetRoot() != "" {
		parts = append(parts, "root:"+vname.GetRoot())
	}
	if vname.GetPath() != "" {
		parts = append(parts, "path:"+vname.GetPath())
	}
	if vname.GetLanguage() != "" {
		parts = append(parts, "lang:"+vname.GetLanguage())
	}
	if len(parts) == 0 {
		return "{empty VName}"
	}
	return strings.Join(parts, ", ")
}

type showUnitCmd struct {
	subcommands.CommandRunBase
}

func (c *showUnitCmd) Run(a subcommands.Application, args []string, env subcommands.Env) int {
	if len(args) != 2 {
		fmt.Fprintf(os.Stderr, "%s %s: expected <kzip_path> <unit_digest>\n", a.GetName(), cmdShow.Name())
		return 1
	}
	kzipPath := args[0]
	unitDigest := args[1]

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

	unit, err := reader.Lookup(unitDigest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error looking up unit digest %s: %v\n", unitDigest, err)
		return 1
	}

	fmt.Printf("Details for Compilation Unit (Digest: %s):\n", unit.Digest)

	if unit.Proto == nil {
		fmt.Println("  CompilationUnit Proto: <nil>")
	} else {
		cu := unit.Proto
		fmt.Printf("  VName: %s\n", formatVName(cu.GetVName()))
		fmt.Printf("  Has Compile Errors: %t\n", cu.GetHasCompileErrors())
		fmt.Printf("  Working Directory: %s\n", cu.GetWorkingDirectory())
		fmt.Printf("  Output Key: %s\n", cu.GetOutputKey())
		fmt.Printf("  Entry Context: %s\n", cu.GetEntryContext())

		fmt.Println("  Source Files:")
		for _, sf := range cu.GetSourceFile() {
			fmt.Printf("    - %s\n", sf)
		}

		fmt.Println("  Arguments:")
		for i, arg := range cu.GetArgument() {
			fmt.Printf("    [%d] %s\n", i, arg)
		}

		fmt.Println("  Required Inputs:")
		for _, ri := range cu.GetRequiredInput() {
			path := "n/a"
			digest := "n/a"
			if ri.GetInfo() != nil {
				path = ri.GetInfo().GetPath()
				digest = ri.GetInfo().GetDigest()
			}
			fmt.Printf("    - Path: %s\n", path)
			fmt.Printf("      Digest: %s\n", digest)
			fmt.Printf("      VName: %s\n", formatVName(ri.GetVName()))
			if len(ri.GetDetails()) > 0 {
				fmt.Println("      Input Details:")
				for _, det := range ri.GetDetails() {
					fmt.Printf("        - TypeURL: %s\n", det.GetTypeUrl())
				}
			}
		}

		fmt.Println("  Environment Variables:")
		for _, envVar := range cu.GetEnvironment() {
			fmt.Printf("    - %s=%s\n", envVar.GetName(), envVar.GetValue())
		}

		fmt.Println("  Details:")
		if len(cu.GetDetails()) == 0 {
			fmt.Println("    <none>")
		}
		for _, detail := range cu.GetDetails() {
			fmt.Printf("    - TypeURL: %s\n", detail.GetTypeUrl())
			fmt.Printf("%s\n", detail.String())
		}
	}

	if unit.Index == nil {
		fmt.Println("  Index: <nil>")
	} else {
		fmt.Println("  Index:")
		fmt.Printf("    Revisions: %s\n", strings.Join(unit.Index.GetRevisions(), ", "))
	}

	return 0
}

var cmdShow = &subcommands.Command{
	UsageLine: "show <kzip_path> <unit_digest>",
	ShortDesc: "Shows information for a specific compilation unit.",
	LongDesc:  "Looks up a compilation unit by its digest and prints its structured details.",
	CommandRun: func() subcommands.CommandRun {
		c := &showUnitCmd{}
		return c
	},
}
