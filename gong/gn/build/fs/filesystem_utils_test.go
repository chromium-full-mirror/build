// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package fs

import (
	"fmt"
	"runtime"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{
			path: "",
			want: "",
		},
		{
			path: "foo/bar.txt",
			want: "foo/bar.txt",
		},
		{
			path: ".",
			want: "",
		},
		{
			path: "..",
			want: "..",
		},
		{
			path: "foo//bar",
			want: "foo/bar",
		},
		{
			path: "/foo/bar",
			want: "/foo/bar",
		},
		{
			path: "//foo",
			want: "//foo",
		},
		{
			path: "foo/..//bar",
			want: "bar",
		},
		{
			path: "foo/../../bar",
			want: "../bar",
		},
		{
			path: "../foo", // Don't go above the root dir.
			want: "../foo",
		},
		{
			path: "//../foo", // Don't go above the root dir.
			want: "//foo",
		},
		{
			path: "..",
			want: "..",
		},
		{
			path: "./././.",
			want: "",
		},
		{
			path: "../../..",
			want: "../../..",
		},
		{
			path: "../",
			want: "../",
		},
		// Backslash normalization.
		{
			path: "foo\\..\\..\\bar",
			want: "../bar",
		},
		// Trailing slashes should get preserved.
		{
			path: "//foo/bar/",
			want: "//foo/bar/",
		},
		// System root relative paths should be safely handled.
		{
			path: "/./foo",
			want: "/foo",
		},
		// Source root relative paths should be safely handled.
		{
			path: "//./foo",
			want: "//foo",
		},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			s := NormalizePath(tc.path)
			if s != tc.want {
				t.Errorf("NormalizePath(%q)=%s; want=%v", tc.path, s, tc.want)
			}
		})
	}
}

func TestNormalizePathWithSourceRoot_NonWindows(t *testing.T) {
	for _, tc := range []struct {
		path       string
		sourceRoot string
		want       string
	}{
		// Go above and outside of the source root.
		{
			path:       "//../foo",
			sourceRoot: "/source/root",
			want:       "/source/foo",
		},
		{
			path:       "//../",
			sourceRoot: "/source/root",
			want:       "/source/",
		},
		{
			path:       "//../foo.txt",
			sourceRoot: "/source/root",
			want:       "/source/foo.txt",
		},
		{
			path:       "//../foo/bar/",
			sourceRoot: "/source/root",
			want:       "/source/foo/bar/",
		},
		// Go above and back into the source root. This should return a system-
		// absolute path. We could arguably return this as a source-absolute path,
		// but that would require additional handling to account for a rare edge
		// case.
		{
			path:       "//../root/foo",
			sourceRoot: "/source/root",
			want:       "/source/root/foo",
		},
		{
			path:       "//../root/foo/bar/",
			sourceRoot: "/source/root",
			want:       "/source/root/foo/bar/",
		},
		// Stay inside the source root
		{
			path:       "//foo/bar",
			sourceRoot: "/source/root",
			want:       "//foo/bar",
		},
		{
			path:       "//foo/bar/",
			sourceRoot: "/source/root",
			want:       "//foo/bar/",
		},
		// The path should not go above the system root.
		{
			path:       "//../../../../../foo/bar",
			sourceRoot: "/source/root",
			want:       "/foo/bar",
		},
		// Test when the source root is the system root.
		{
			path:       "//../foo/bar/",
			sourceRoot: "/",
			want:       "/foo/bar/",
		},
		{
			path:       "//../",
			sourceRoot: "/",
			want:       "/",
		},
		{
			path:       "//../foo.txt",
			sourceRoot: "/",
			want:       "/foo.txt",
		},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			s := normalizePathWithSourceRoot(tc.path, tc.sourceRoot, false)
			if s != tc.want {
				t.Errorf("normalizePathWithSourceRoot(%q, %q, isWindows=false)=%s; want=%v", tc.path, tc.sourceRoot, s, tc.want)
			}
		})
	}
}

func TestNormalizePathWithSourceRoot_Windows(t *testing.T) {
	for _, tc := range []struct {
		path       string
		sourceRoot string
		want       string
	}{
		// Go above and outside of the source root.
		{
			path:       "//../foo",
			sourceRoot: "/C:/source/root",
			want:       "/C:/source/foo",
		},
		{
			path:       "//../foo",
			sourceRoot: "C:\\source\\root",
			want:       "/C:/source/foo",
		},
		{
			path:       "//../",
			sourceRoot: "/C:/source/root",
			want:       "/C:/source/",
		},
		{
			path:       "//../foo.txt",
			sourceRoot: "/C:/source/root",
			want:       "/C:/source/foo.txt",
		},
		{
			path:       "//../foo/bar/",
			sourceRoot: "/C:/source/root",
			want:       "/C:/source/foo/bar/",
		},
		// Go above and back into the source root. This should return a system-
		// absolute path. We could arguably return this as a source-absolute path,
		// but that would require additional handling to account for a rare edge
		// case.
		{
			path:       "//../root/foo",
			sourceRoot: "/C:/source/root",
			want:       "/C:/source/root/foo",
		},
		{
			path:       "//../root/foo/bar/",
			sourceRoot: "/C:/source/root",
			want:       "/C:/source/root/foo/bar/",
		},
		// Stay inside the source root
		{
			path:       "//foo/bar",
			sourceRoot: "/C:/source/root",
			want:       "//foo/bar",
		},
		{
			path:       "//foo/bar/",
			sourceRoot: "/C:/source/root",
			want:       "//foo/bar/",
		},
		// The path should not go above the system root. Note that on Windows, this
		// will consume the drive (C:).
		{
			path:       "//../../../../../foo/bar",
			sourceRoot: "/C:/source/root",
			want:       "/foo/bar",
		},
		// Test when the source root is the letter drive.
		{
			path:       "//../foo",
			sourceRoot: "/C:",
			want:       "/foo",
		},
		{
			path:       "//../foo",
			sourceRoot: "C:",
			want:       "/foo",
		},
		{
			path:       "//../foo",
			sourceRoot: "/",
			want:       "/foo",
		},
		{
			path:       "//../",
			sourceRoot: "/C:",
			want:       "/",
		},
		{
			path:       "//../foo.txt",
			sourceRoot: "/C:",
			want:       "/foo.txt",
		},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			s := normalizePathWithSourceRoot(tc.path, tc.sourceRoot, true)
			if s != tc.want {
				t.Errorf("normalizePathWithSourceRoot(%q, %q, isWindows=true)=%s; want=%v", tc.path, tc.sourceRoot, s, tc.want)
			}
		})
	}
}

func TestRebasePath(t *testing.T) {
	sourceRoot := "/source/root"

	for _, tc := range []struct {
		input      string
		destDir    string
		sourceRoot string
		want       string
		wantErr    bool
	}{
		// Degenerate case.
		{"//", "//", sourceRoot, ".", false},
		{"//foo/bar/", "//foo/bar/", sourceRoot, ".", false},

		// Going up the tree.
		{"//foo", "//bar/", sourceRoot, "../foo", false},
		{"//foo/", "//bar/", sourceRoot, "../foo/", false},
		{"//foo", "//bar/moo", sourceRoot, "../../foo", false},
		{"//foo/", "//bar/moo", sourceRoot, "../../foo/", false},

		// Going down the tree.
		{"//foo/bar", "//", sourceRoot, "foo/bar", false},
		{"//foo/bar/", "//", sourceRoot, "foo/bar/", false},

		// Going up and down the tree.
		{"//foo/bar", "//a/b/", sourceRoot, "../../foo/bar", false},
		{"//foo/bar/", "//a/b/", sourceRoot, "../../foo/bar/", false},

		// Sharing prefix.
		{"//a/foo", "//a/", sourceRoot, "foo", false},
		{"//a/foo", "//a", sourceRoot, "foo", false},
		{"//a/foo/", "//a/", sourceRoot, "foo/", false},
		{"//a/b/foo", "//a/b/", sourceRoot, "foo", false},
		{"//a/b/foo/", "//a/b/", sourceRoot, "foo/", false},
		{"//a/b/foo/bar", "//a/b/", sourceRoot, "foo/bar", false},
		{"//a/b/foo/bar/", "//a/b/", sourceRoot, "foo/bar/", false},
		{"//foo/bar", "//foo/bar/", sourceRoot, ".", false},
		{"//foo", "//foo/bar/", sourceRoot, "..", false},
		{"//foo/", "//foo/bar/", sourceRoot, "../", false},

		// Check when only input is system-absolute.
		{"/source/root/foo", "//", "/source/root", "foo", false},
		{"/source/root/foo/", "//", "/source/root", "foo/", false},
		{"/builddir/Out/Debug", "//", "/source/root", "../../builddir/Out/Debug", false},
		{"/builddir/Out/Debug", "//", "/source/root/foo", "../../../builddir/Out/Debug", false},
		{"/builddir/Out/Debug/", "//", "/source/root/foo", "../../../builddir/Out/Debug/", false},
		{"/path/to/foo", "//", "/source/root", "../../path/to/foo", false},
		{"/path/to/foo", "//a", "/source/root", "../../../path/to/foo", false},
		{"/path/to/foo", "//a/b", "/source/root", "../../../../path/to/foo", false},

		// Check when only destDir is system-absolute.
		{"//", "/source/root", "/source/root", ".", false},
		{"//foo", "/source/root", "/source/root", "foo", false},
		{"//foo", "/source/root/bar", "/source/root", "../foo", false},
		{"//foo", "/other/source/root", "/source/root", "../../../source/root/foo", false},
		{"//foo", "/other/source/root/bar", "/source/root", "../../../../source/root/foo", false},

		// Check when input and destDir are both system-absolute. Also,
		// in this case sourceRoot is never used so set it to a dummy
		// value.
		{"/source/root/foo", "/source/root", "/x/y/z", "foo", false},
		{"/source/root/foo/", "/source/root", "/x/y/z", "foo/", false},
		{"/builddir/Out/Debug", "/source/root", "/x/y/z", "../../builddir/Out/Debug", false},
		{"/builddir/Out/Debug", "/source/root/foo", "/source/root/foo", "../../../builddir/Out/Debug", false},
		{"/builddir/Out/Debug/", "/source/root/foo", "/source/root/foo", "../../../builddir/Out/Debug/", false},
		{"/path/to/foo", "/source/root", "/x/y/z", "../../path/to/foo", false},
		{"/path/to/foo", "/source/root/a", "/x/y/z", "../../../path/to/foo", false},
		{"/path/to/foo", "/source/root/a/b", "/x/y/z", "../../../../path/to/foo", false},

		// Should error if sourceRoot empty and mixing source-relative and absolute paths.
		{"foo/bar.txt", "//foo/", "", "", true},
		{"//foo/bar.txt", "/foo/", "", "", true},
	} {
		t.Run(fmt.Sprintf("%s_%s", tc.input, tc.destDir), func(t *testing.T) {
			destDir, err := MakeSourceDir(tc.destDir)
			if err != nil {
				t.Fatalf("MakeSourceDir(%q) failed: %v", tc.destDir, err)
			}

			got, err := RebasePath(tc.input, destDir, tc.sourceRoot)

			if tc.wantErr {
				if err == nil {
					t.Errorf("RebasePath(%q, %q, %q)=%q,nil; want error", tc.input, tc.destDir, tc.sourceRoot, got)
				}
				return
			}

			if err != nil {
				t.Errorf("RebasePath(%q, %q, %q) returned error %v, want success", tc.input, tc.destDir, tc.sourceRoot, err)
			}

			if got != tc.want {
				t.Errorf("RebasePath(%q, %q, %q)=%q; want %q", tc.input, tc.destDir, tc.sourceRoot, got, tc.want)
			}
		})
	}
}

func TestRebasePath_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("These tests are Windows only")
	}

	for _, tc := range []struct {
		input      string
		destDir    string
		sourceRoot string
		want       string
		wantErr    bool
	}{
		// Test corrections while rebasing Windows-style absolute paths.
		{"C:/path/to/foo", "//a/b", "/C:/source/root", "../../../../path/to/foo", false},
		{"/C:/path/to/foo", "//a/b", "C:/source/root", "../../../../path/to/foo", false},
		{"/C:/path/to/foo", "//a/b", "/c:/source/root", "../../../../path/to/foo", false},
		{"/c:/path/to/foo", "//a/b", "c:/source/root", "../../../../path/to/foo", false},
		{"/c:/path/to/foo", "//a/b", "C:/source/root", "../../../../path/to/foo", false},

		// Different drive letters not yet supported.
		{"C:/path/to/foo", "//a/b", "D:/source/root", "C:/path/to/foo", true},
		{"D:/path/to/foo", "//a/b", "C:/source/root", "D:/path/to/foo", true},
		{"/E:/path/to/foo", "//a/b", "/c:/source/root", "E:/path/to/foo", true},
		{"/e:/path/to/foo", "//a/b", "c:/source/root", "E:/path/to/foo", true},
		{"/c:/path/to/foo", "//a/b", "D:/source/root", "C:/path/to/foo", true},
	} {
		t.Run(fmt.Sprintf("%s_%s", tc.input, tc.destDir), func(t *testing.T) {
			destDir, err := MakeSourceDir(tc.destDir)
			if err != nil {
				t.Fatalf("MakeSourceDir(%q) failed: %v", tc.destDir, err)
			}

			got, err := RebasePath(tc.input, destDir, tc.sourceRoot)

			if tc.wantErr {
				if err == nil {
					t.Errorf("RebasePath(%q, %q, %q)=%q,nil; want error", tc.input, tc.destDir, tc.sourceRoot, got)
				}
				return
			}

			if err != nil {
				t.Errorf("RebasePath(%q, %q, %q) returned error %v, want success", tc.input, tc.destDir, tc.sourceRoot, err)
			}

			if got != tc.want {
				t.Errorf("RebasePath(%q, %q, %q)=%q; want %q", tc.input, tc.destDir, tc.sourceRoot, got, tc.want)
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	for _, tc := range []struct {
		input      string
		sourceRoot string
		want       string
	}{
		{input: "", want: ""},
		// No source root, Unix style path. Remains unchanged.
		{input: "/x/y", want: "/x/y"},
		// No source root, Windows style path. Leading / stripped regardless of platform.
		{input: "/C:/x/y", want: "C:/x/y"},
		{input: "/C:/", want: "C:/"},
		// Source root resolves //.
		{input: "//foo/bar", want: "foo/bar"},
		{input: "//foo/bar", sourceRoot: "/src", want: "/src/foo/bar"},
		{input: "//foo/bar", sourceRoot: "C:/src", want: "C:/src/foo/bar"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			p := ResolvePath(tc.input, tc.sourceRoot)
			if p != tc.want {
				t.Errorf("ResolvePath(%q, %q)=%s; want=%v", tc.input, tc.sourceRoot, p, tc.want)
			}
		})
	}
}

func TestDirectoryWithNoLastSlash(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{
			path: "",
			want: "",
		},
		{
			path: "/",
			want: "/.",
		},
		{
			path: "//",
			want: "//.",
		},
		{
			path: "//foo/",
			want: "//foo",
		},
		{
			path: "/bar/",
			want: "/bar",
		},
		{
			path: "//foo",
			want: "//foo",
		},
		{
			path: "/./foo",
			want: "/./foo",
		},
		{
			path: "//./foo",
			want: "//./foo",
		},
		// Only the final slash should be removed.
		{
			path: "/bar//",
			want: "/bar/",
		},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			s := DirectoryWithNoLastSlash(tc.path)
			if s != tc.want {
				t.Errorf("DirectoryWithNoLastSlash(%q)=%s; want=%v", tc.path, s, tc.want)
			}
		})
	}
}
