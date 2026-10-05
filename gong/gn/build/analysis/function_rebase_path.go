// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"strings"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

// Port of C++ GN's logic to determine whether inputs to rebase_path() look like a directory.
// Basically, does it end with a slash (optionally with dots after) or is it all dots.
func valueLooksLikeDir(value string) bool {
	trimmed := strings.TrimRight(value, ".")
	return trimmed == "" || strings.HasSuffix(trimmed, "/")
}

// matchSlashEnding ensures the output path has a trailing slash iff the input path does.
// TODO: what happens with windows style paths?
func matchSlashEnding(input, output string) string {
	if strings.HasSuffix(input, "/") {
		if !strings.HasSuffix(output, "/") {
			return output + "/"
		}
	} else if outputCut, ok := strings.CutSuffix(output, "/"); ok {
		return outputCut
	}
	return output
}

func rebaseOnePath(ctx *scopeContext, path string, fromDir, destDir fs.SourceDir) (resolve.Value, error) {
	origPath := path
	if valueLooksLikeDir(path) {
		sourceDir, err := fromDir.ResolveRelativeDir(path)
		if err != nil {
			return nil, err
		}
		path = sourceDir.Path()
	} else {
		sourceFile, err := fromDir.ResolveRelativeFile(path)
		if err != nil {
			return nil, err
		}
		path = sourceFile.Filename()
	}

	rebased, err := fs.RebasePath(path, destDir, ctx.settings.buildSettings.RootPath)
	if err != nil {
		return nil, err
	}
	rebased = matchSlashEnding(origPath, rebased)
	return resolve.NewOriginlessStringValue(rebased), nil
}

func systemAbsoluteOnePath(ctx *scopeContext, path string, fromDir fs.SourceDir) (resolve.Value, error) {
	origPath := path
	var rebased string
	if valueLooksLikeDir(path) {
		sourceDir, err := fromDir.ResolveRelativeDir(path)
		if err != nil {
			return nil, err
		}
		rebased = ctx.settings.buildSettings.FullDirPath(sourceDir)
	} else {
		sourceFile, err := fromDir.ResolveRelativeFile(path)
		if err != nil {
			return nil, err
		}
		rebased = ctx.settings.buildSettings.FullPath(sourceFile)
	}
	rebased = matchSlashEnding(origPath, rebased)
	return resolve.NewOriginlessStringValue(rebased), nil
}

type rebasePathFunction struct{}

func (rebasePathFunction) IsTarget() bool { return false }
func (rebasePathFunction) HelpShort() string {
	return "rebase_path: Rebase a file or directory to another location."
}
func (rebasePathFunction) Help() string {
	return `rebase_path: Rebase a file or directory to another location.

  converted = rebase_path(input,
                          new_base = "",
                          current_base = ".")

  Takes a string argument representing a file name, or a list of such strings
  and converts it/them to be relative to a different base directory.

  When invoking the compiler or scripts, GN will automatically convert sources
  and include directories to be relative to the build directory. However, if
  you're passing files directly in the "args" array or doing other manual
  manipulations where GN doesn't know something is a file name, you will need
  to convert paths to be relative to what your tool is expecting.

  The common case is to use this to convert paths relative to the current
  directory to be relative to the build directory (which will be the current
  directory when executing scripts).

  If you want to convert a file path to be source-absolute (that is, beginning
  with a double slash like "//foo/bar"), you should use the get_path_info()
  function. This function won't work because it will always make relative
  paths, and it needs to support making paths relative to the source root, so
  it can't also generate source-absolute paths without more special-cases.

Arguments

  input
      A string or list of strings representing file or directory names. These
      can be relative paths ("foo/bar.txt"), system absolute paths
      ("/foo/bar.txt"), or source absolute paths ("//foo/bar.txt").

  new_base
      The directory to convert the paths to be relative to. This can be an
      absolute path or a relative path (which will be treated as being relative
      to the current BUILD-file's directory).

      As a special case, if new_base is the empty string (the default), all
      paths will be converted to system-absolute native style paths with system
      path separators. This is useful for invoking external programs.

  current_base
      Directory representing the base for relative paths in the input. If this
      is not an absolute path, it will be treated as being relative to the
      current build file. Use "." (the default) to convert paths from the
      current BUILD-file's directory.

Return value

  The return value will be the same type as the input value (either a string or
  a list of strings). All relative and source-absolute file names will be
  converted to be relative to the requested output System-absolute paths will
  be unchanged.

  Whether an output path will end in a slash will match whether the
  corresponding input path ends in a slash. It will return "." or "./"
  (depending on whether the input ends in a slash) to avoid returning empty
  strings. This means if you want a root path ("//" or "/") not ending in a
  slash, you can add a dot ("//.").

Example

  # Convert a file in the current directory to be relative to the build
  # directory (the current dir when executing compilers and scripts).
  foo = rebase_path("myfile.txt", root_build_dir)
  # might produce "../../project/myfile.txt".

  # Convert a file to be system absolute:
  foo = rebase_path("myfile.txt")
  # Might produce "D:\\source\\project\\myfile.txt" on Windows or
  # "/home/you/source/project/myfile.txt" on Linux.

  # Typical usage for converting to the build directory for a script.
  action("myscript") {
    # Don't convert sources, GN will automatically convert these to be relative
    # to the build directory when it constructs the command line for your
    # script.
    sources = [ "foo.txt", "bar.txt" ]

    # Extra file args passed manually need to be explicitly converted
    # to be relative to the build directory:
    args = [
      "--data",
      rebase_path("//mything/data/input.dat", root_build_dir),
      "--rel",
      rebase_path("relative_path.txt", root_build_dir)
    ] + rebase_path(sources, root_build_dir)
  }
`
}

func (rebasePathFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value) (resolve.Value, error) {
	ctx, err := contextFromScope(scope)
	if err != nil {
		return nil, err
	}

	if len(args) < 1 || len(args) > 3 {
		return nil, resolve.ArgumentCountError{
			Call: call,
			Msg:  "Wrong # of arguments for rebase_path.",
		}
	}

	var newBase string
	if len(args) >= 2 {
		val, err := resolve.AsValue[*resolve.StringValue](args[1])
		if err != nil {
			return nil, err
		}
		newBase = val.RawGNString()
	}

	fromDir := ctx.sourceDir
	if len(args) >= 3 {
		val, err := resolve.AsValue[*resolve.StringValue](args[2])
		if err != nil {
			return nil, err
		}
		fromDir, err = ctx.sourceDir.ResolveRelativeDir(val.RawGNString())
		if err != nil {
			return nil, err
		}
	}

	if newBase != "" {
		destDir, err := ctx.sourceDir.ResolveRelativeDir(newBase)
		if err != nil {
			return nil, err
		}
		return rebaseInput(args[0], func(path string) (resolve.Value, error) {
			return rebaseOnePath(ctx, path, fromDir, destDir)
		})
	}

	return rebaseInput(args[0], func(path string) (resolve.Value, error) {
		return systemAbsoluteOnePath(ctx, path, fromDir)
	})
}

func rebaseInput(input resolve.Value, rebaser func(path string) (resolve.Value, error)) (resolve.Value, error) {
	switch v := input.(type) {
	case *resolve.ListValue:
		listResult := make([]resolve.Value, 0, v.Len())
		for item := range v.Values() {
			sv, err := resolve.AsValue[*resolve.StringValue](item)
			if err != nil {
				return nil, err
			}
			converted, err := rebaser(sv.RawGNString())
			if err != nil {
				return nil, err
			}
			listResult = append(listResult, converted)
		}
		// TODO: Implement NewListValueAt to attach origin to list values if needed.
		return resolve.NewOriginlessListValue(listResult), nil

	case *resolve.StringValue:
		return rebaser(v.RawGNString())

	default:
		return nil, resolve.TypeError{
			Value: v,
			Msg:   "rebase_path requires a list or a string.",
		}
	}
}
