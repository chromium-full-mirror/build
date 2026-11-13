// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Command goanalyzers is a `go vet -vettool` that runs this repo's custom
// analyzers. Each analyzer can be selected with a flag of its name, e.g.
//
//	go vet -vettool=goanalyzers -subtestnames ./...
//
// Without any such flag, all analyzers run.
package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	"go.chromium.org/build/infra/goanalyzers/subtestnames"
)

func main() {
	multichecker.Main(
		subtestnames.Analyzer,
	)
}
