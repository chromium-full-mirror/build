// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package fs

import (
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

func endsWithSlash(path string) bool {
	return path != "" && path[len(path)-1] == '/'
}

// IsPathSourceAbsolute returns true if the input string is source-absolute. Source-absolute
// paths begin with two forward slashes and resolve as if they are
// relative to the source root.
func IsPathSourceAbsolute(path string) bool {
	return strings.HasPrefix(path, "//")
}

// NormalizePath collapses "." and sequential "/"s and evaluates "..". |path| may be
// system-absolute, source-absolute, or relative. |path| will retain its relativity,
// use NormalizePathWithSourceRoot if a different source root is desired.
func NormalizePath(path string) string {
	return NormalizePathWithSourceRoot(path, "")
}

// NormalizePathWithSourceRoot is same as NormalizePath, but if |path| is source-absolute
// and |sourceRoot| is non-empty, |path| may be system absolute after this
// function returns, if |path| references the filesystem outside of
// |sourceRoot| (ex. path = "//.."). In this case on Windows, |path| will have
// a leading slash. Otherwise, |path| will retain its relativity. |sourceRoot|
// must not end with a slash.
func NormalizePathWithSourceRoot(path, sourceRoot string) string {
	return normalizePathWithSourceRoot(path, sourceRoot, runtime.GOOS == "windows")
}

func normalizePathWithSourceRoot(path, sourceRoot string, isWindows bool) string {
	// We can rely on Go's filepath.Clean for a rough approximation of GN's
	// path normalization, rather than porting over the fully bespoke
	// character-by-character logic. However this results in some notable
	// differences to account for.

	// Firstly, filepath.Clean can't handle "//" source-absolute paths.
	// Keep track of whether this path is source-absolute to trim later.
	restoreLeadingSlash := strings.HasPrefix(path, "//")

	// Secondly, GN normalizes all backwards to forward slashes.
	// Do this after checking for "//" prefix, because any other variation
	// does not have special meaning i.e. "\\foo" is treated as filesystem
	// absolute and therefore should be normalized to "/foo".
	path = strings.ReplaceAll(path, "\\", "/")
	sourceRoot = strings.ReplaceAll(sourceRoot, "\\", "/")

	// Thirdly, GN normalization requires trailing slashes to be preserved.
	restoreTrailingSlash := endsWithSlash(path)

	// Next, behavior depends on whether sourceRoot is set.
	if sourceRoot == "" {
		// If no sourceRoot, we can perform a clean now.
		// Temporarily trimming "//" to "/" preserves source-absolute paths
		// e.g. "//../foo" -> "/../foo" -> "/foo" -> "//foo"
		//       ^ remove here                        ^ restore here
		if restoreLeadingSlash {
			path = strings.TrimPrefix(path, "/")
		}
		path = filepath.Clean(path)
	} else {
		// Only on Windows, GN ensures sourceRoot has slash prefix.
		if isWindows && !strings.HasPrefix(sourceRoot, "/") {
			sourceRoot = "/" + sourceRoot
		}

		// If sourceRoot set, first figure out if we stay as source-absolute.
		// Only if we will exit source-absolute, then consider the sourceRoot.
		if strings.HasPrefix(filepath.Clean(strings.TrimPrefix(path, "//")), "..") {
			path = filepath.Join(sourceRoot, path)
			restoreLeadingSlash = false
		} else if restoreLeadingSlash {
			path = strings.TrimPrefix(path, "/")
		}
		path = filepath.Clean(path)
	}

	// filepath.Clean uses os.PathSeparator which is undesirable on Windows.
	// Always call filepath.ToSlash (it's a no-op on non-Windows).
	path = filepath.ToSlash(path)

	// filepath.Clean will return "." if the result is empty.
	// However, GN normalization should result in an empty path.
	if path == "." {
		return ""
	}

	if restoreLeadingSlash {
		path = "/" + path
	}
	if restoreTrailingSlash && !strings.HasSuffix(path, "/") {
		path = path + "/"
	}
	return path
}

// RebasePath takes a path, input, and makes it relative to the given
// directory destDir. Both inputs may be source-relative (e.g. begins
// with "//") or may be absolute.
//
// If supplied, the sourceRoot parameter is the absolute path to
// the source root and not end in a slash. Unless you know that the
// inputs are always source relative, this should be supplied.
func RebasePath(input string, destDir SourceDir, sourceRoot string) (string, error) {
	// Keep track of whether the input ends with a slash.
	// This implementation defers to the inbuilt filepath.Clean to perform most of the
	// work of cleaning paths, but it also strips trailing slashes which are used in GN
	// to indicate something is a directory.
	restoreTrailingSlash := endsWithSlash(input)
	dest := destDir.Path()

	if IsPathSourceAbsolute(input) && IsPathSourceAbsolute(dest) {
		// If both paths are source-relative, we can just trim "//" and use filepath.Rel
		// without needing to consider sourceRoot.
		// (Especially needed on Windows as "//" may be interpreted as UNC network share.)
		input = strings.TrimPrefix(input, "//")
		dest = strings.TrimPrefix(dest, "//")
	} else {
		// Otherwise, one or both paths are absolute.
		// To perform a relative comparison, if there's a source-relative path,
		// make it absolute using sourceRoot.
		if IsPathSourceAbsolute(input) {
			if sourceRoot == "" {
				return "", fmt.Errorf("can't rebase source-relative to absolute path without sourceRoot")
			}
			input = filepath.Join(sourceRoot, strings.TrimPrefix(input, "//"))
		}
		if IsPathSourceAbsolute(dest) {
			if sourceRoot == "" {
				return "", fmt.Errorf("can't rebase absolute to source-relative path without sourceRoot")
			}
			dest = filepath.Join(sourceRoot, strings.TrimPrefix(dest, "//"))
		}
	}

	// On Windows, SourceDir system-absolute paths start with /, e.g. "/C:/foo/bar".
	if runtime.GOOS == "windows" {
		if len(input) > 2 && input[2] == ':' {
			input = input[1:]
		}
		if len(dest) > 2 && dest[2] == ':' {
			dest = dest[1:]
		}
	}

	relPath, err := filepath.Rel(dest, input)
	if err != nil {
		// TODO: Handle cross-drive relative paths on windows.
		return "", err
	}

	ret := filepath.ToSlash(filepath.Clean(relPath))
	if restoreTrailingSlash && ret != "." && !endsWithSlash(ret) {
		ret += "/"
	}
	return ret, nil
}

// ResolvePath resolves source file or directory relative to some given source root.
// (This does not have to be the source root of the build tree.)
func ResolvePath(input, sourceRoot string) string {
	if input == "" {
		return ""
	}
	if !IsPathSourceAbsolute(input) {
		if len(input) > 2 && input[2] == ':' {
			// Windows path, strip the leading slash.
			return input[1:]
		}
		return input
	}
	// Make sure to strip the double-leading slash for source-relative paths.
	return filepath.ToSlash(path.Join(sourceRoot, strings.TrimPrefix(input, "//")))
}

// DirectoryWithNoLastSlash prepares a directory path string with its last
// slash removed if in the string, allowing for safe naive string concatenation
// with another path component.
//
// It ensures the path does not have a trailing slash, and it handles the special
// cases of the system root ("/") and source root ("//") by converting them to
// "/." and "//." respectively. This prevents incorrect path joining.
// (For example, naively joining "/" and "foo" would result in "//foo", which is
// source-absolute path, whereas joining "/." and "foo" instead would correctly
// result in "/./foo", which maintains the correct relativity.)
//
// This function does not attempt to canonicalize the path. For example,
// "a/./b" is returned as-is. Use [NormalizePath] instead if this is desired.
//
// Consequently, calling with the path "/bar//" would return the result "/bar/".
// This is expected because only the last slash is removed, not all trailing
// slashes. However, this also maintains the requirement of safe naive path
// component concatenation, as "/bar/" + "/baz" would result in "/bar//baz".
//
// (While on the surface this may appear to be a deviation from C++ GN's
// DirectoryWithNoLastSlash, the aforementioned function take C++ GN's SourceDir
// class as an input, which pre-normalizes directory paths. Here, we deliberately
// do not ensure that the input has been pre-normalized. Callers that desire
// such behavior are expected to call [NormalizePath] ahead of time themselves.)
func DirectoryWithNoLastSlash(path string) string {
	if path == "/" {
		return "/."
	}
	if path == "//" {
		return "//."
	}
	if strings.HasSuffix(path, "/") {
		return path[:len(path)-1]
	}
	return path
}

// cleanInputPath cleans the path, accepting normal Unix paths,
// or if on Windows, also handles Windows-style absolute paths "C:\foo", "C:foo", and GN-style "/C:/foo".
func cleanInputPath(path string) string {
	if runtime.GOOS == "windows" {
		// If absolute GN-style path, need to strip "/" to use with filepath.
		if len(path) > 3 && path[0] == '/' && path[2] == ':' {
			path = path[1:]
		}
		// Need to accept weird paths like "C:foo\bar".
		if len(path) >= 2 && path[1] == ':' {
			if len(path) == 2 || (path[2] != '/' && path[2] != '\\') {
				path = path[:2] + "/" + path[2:]
			}
		}
	}
	return filepath.Clean(path)
}
