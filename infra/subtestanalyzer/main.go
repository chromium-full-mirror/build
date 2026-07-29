// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/singlechecker"
)

var Analyzer = &analysis.Analyzer{
	Name: "subtestnames",
	Doc:  "checks that Go subtest names follow https://google.github.io/styleguide/go/decisions#subtest-names",
	Run:  run,
}

func main() {
	singlechecker.Main(Analyzer)
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		filename := pass.Fset.Position(file.Pos()).Filename
		if !strings.HasSuffix(filename, "_test.go") {
			continue
		}

		ast.Inspect(file, func(n ast.Node) bool {
			checkTableName(pass, n)
			checkTRun(pass, n)
			return true
		})
	}
	return nil, nil
}

// checkTableName checks KeyValueExpr in composite literals (e.g. name: "literal with space/slash").
func checkTableName(pass *analysis.Pass, n ast.Node) {
	kve, ok := n.(*ast.KeyValueExpr)
	if !ok {
		return
	}
	key, ok := kve.Key.(*ast.Ident)
	if !ok || key.Name != "name" {
		return
	}
	lit, ok := kve.Value.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return
	}
	val := strings.Trim(lit.Value, "`\"")
	if strings.Contains(val, " ") || strings.Contains(val, "/") {
		pass.Reportf(lit.Pos(), "table case `name` %q contains spaces or slashes per https://google.github.io/styleguide/go/decisions#subtest-names", val)
	}
}

// checkTRun checks t.Run(...) call expressions for invalid subtest names.
func checkTRun(pass *analysis.Pass, n ast.Node) {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Run" {
		return
	}
	if len(call.Args) < 1 {
		return
	}

	nameArg := call.Args[0]

	// t.Run(tc.desc, ...) or t.Run(tt.desc, ...)
	if selExpr, ok := nameArg.(*ast.SelectorExpr); ok {
		if selExpr.Sel.Name == "desc" || selExpr.Sel.Name == "description" {
			pass.Reportf(selExpr.Pos(), "uses .%s field instead of .name in t.Run per https://google.github.io/styleguide/go/decisions#subtest-names", selExpr.Sel.Name)
		}
	}

	// t.Run("inline string with space or slash", ...)
	if lit, ok := nameArg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		val := strings.Trim(lit.Value, "`\"")
		if strings.Contains(val, " ") || strings.Contains(val, "/") {
			pass.Reportf(lit.Pos(), "subtest name %q contains spaces or slashes per https://google.github.io/styleguide/go/decisions#subtest-names", val)
		}
	}
}
