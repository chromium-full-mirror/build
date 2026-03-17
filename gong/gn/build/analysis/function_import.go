// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type importFunction struct{}

func (importFunction) IsTarget() bool { return false }
func (importFunction) HelpShort() string {
	return "import: Import a file into the current scope."
}
func (importFunction) Help() string {
	return `import: Import a file into the current scope.

  The import command loads the rules and variables resulting from executing the
  given file into the current scope.

  By convention, imported files are named with a .gni extension.

  An import is different than a C++ "include". The imported file is executed in
  a standalone environment from the caller of the import command. The results
  of this execution are cached for other files that import the same .gni file.

  Note that you can not import a BUILD.gn file that's otherwise used in the
  build. Files must either be imported or implicitly loaded as a result of deps
  rules, but not both.

  The imported file's scope will be merged with the scope at the point import
  was called. If there is a conflict (both the current scope and the imported
  file define some variable or rule with the same name but different value), a
  runtime error will be thrown. Therefore, it's good practice to minimize the
  stuff that an imported file defines.

  Variables and templates beginning with an underscore '_' are considered
  private and will not be imported. Imported files can use such variables for
  internal computation without affecting other files.

Examples

  import("//build/rules/idl_compilation_rule.gni")

  # Looks in the current directory.
  import("my_vars.gni")
`
}

func (importFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value) (resolve.Value, error) {
	ctx, err := contextFromScope(scope)
	if err != nil {
		return nil, err
	}

	if len(args) != 1 {
		return nil, resolve.ArgumentCountError{
			OriginFunction: resolve.OriginFunction{Call: call},
			Msg:            "This function requires a single string argument.",
		}
	}

	argValue, err := resolve.AsValue[*resolve.StringValue](args[0])
	if err != nil {
		return nil, err
	}
	path := argValue.RawGNString()

	sourceFile, err := ctx.sourceDir.ResolveRelativeFile(path)
	if err != nil {
		return nil, err
	}

	err = ctx.settings.importManager.DoImport(sourceFile, call, scope, ctx.settings)
	if err != nil {
		return nil, err
	}

	return nil, nil
}
