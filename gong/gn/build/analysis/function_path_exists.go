// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"os"
	"strings"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type pathExistsFunction struct{}

func (pathExistsFunction) HelpShort() string {
	return "path_exists: Returns true if the given path exists."
}
func (pathExistsFunction) Help() string {
	return `path_exists: Returns whether the given path exists.

  path_exists(path)

Examples:
  path_exists("//")  # true
  path_exists("BUILD.gn")  # true
  path_exists("/abs-non-existent")  # false
`
}
func (pathExistsFunction) IsTarget() bool { return false }
func (pathExistsFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value) (resolve.Value, error) {
	if len(args) != 1 {
		return nil, resolve.ArgumentCountError{
			Call: call,
			Msg:  "Expecting exactly one argument.",
		}
	}
	pathVal, err := resolve.AsValue[*resolve.StringValue](args[0])
	if err != nil {
		return nil, err
	}

	ctx, err := contextFromScope(scope)
	if err != nil {
		return nil, err
	}

	fsRoot := ctx.settings.buildSettings.RootPath
	gnPath := pathVal.RawGNString()

	// GN paths are normalized to always end in / if a dir, so we
	// decide how to convert into FS path depending on the suffix.
	var fsPath string
	if strings.HasSuffix(gnPath, "/") {
		sourceDir, err := ctx.sourceDir.ResolveRelativeDir(gnPath)
		if err != nil {
			return nil, err
		}
		fsPath = sourceDir.Resolve(fsRoot)
	} else {
		sourceFile, err := ctx.sourceDir.ResolveRelativeFile(gnPath)
		if err != nil {
			return nil, err
		}
		fsPath = sourceFile.Resolve(fsRoot)
	}

	_, err = os.Stat(fsPath)
	return resolve.NewBooleanValueAt(call, err == nil), nil
}
