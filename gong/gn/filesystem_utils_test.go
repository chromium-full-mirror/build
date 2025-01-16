// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package gn

import "testing"

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
	} {
		tc := tc
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
		tc := tc
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
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			s := normalizePathWithSourceRoot(tc.path, tc.sourceRoot, true)
			if s != tc.want {
				t.Errorf("normalizePathWithSourceRoot(%q, %q, isWindows=true)=%s; want=%v", tc.path, tc.sourceRoot, s, tc.want)
			}
		})
	}
}
