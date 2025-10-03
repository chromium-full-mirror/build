// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package fs

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
	for _, tc := range []struct {
		name    string
		input   string
		destDir string
		want    string
		wantErr bool
	}{
		{
			name:    "simple subdirectory",
			input:   "//foo/bar/baz.txt",
			destDir: "//foo/",
			want:    "bar/baz.txt",
			wantErr: false,
		},
		{
			name:  "simple subdirectory with no slash",
			input: "//foo/bar/baz.txt",
			// in case destDir is from DirectoryWithNoLastSlash
			destDir: "//foo",
			want:    "bar/baz.txt",
			wantErr: false,
		},
		{
			name:    "peer directories",
			input:   "//foo/bar/baz.txt",
			destDir: "//foo/qux/",
			want:    "../bar/baz.txt",
			wantErr: false,
		},
		{
			name:  "peer directories with no slash",
			input: "//foo/bar/baz.txt",
			// in case destDir is from DirectoryWithNoLastSlash
			destDir: "//foo/qux",
			want:    "../bar/baz.txt",
			wantErr: false,
		},
		{
			name:    "parent directory",
			input:   "//foo/",
			destDir: "//foo/bar/baz/",
			want:    "../..",
			wantErr: false,
		},
		{
			name:    "identical paths",
			input:   "//foo/bar.txt",
			destDir: "//foo/bar.txt",
			want:    ".",
			wantErr: false,
		},
		{
			name:    "to root",
			input:   "//foo/bar.txt",
			destDir: "//",
			want:    "foo/bar.txt",
			wantErr: false,
		},
		{
			name:  "to root with no slash",
			input: "//foo/bar.txt",
			// in case destDir is from DirectoryWithNoLastSlash
			destDir: "//.",
			want:    "foo/bar.txt",
			wantErr: false,
		},
		{
			name:    "from root",
			input:   "//",
			destDir: "//foo/bar/",
			want:    "../..",
			wantErr: false,
		},
		{
			name:    "error relative input not yet supported",
			input:   "foo/bar.txt",
			destDir: "//foo/",
			wantErr: true,
		},
		{
			name:    "error relative destDir not yet supported",
			input:   "//foo/bar.txt",
			destDir: "foo/",
			wantErr: true,
		},
		{
			name:    "error system absolute input not yet supported",
			input:   "/foo/bar.txt",
			destDir: "//foo/",
			wantErr: true,
		},
		{
			name:    "error system absolute destDir not yet supported",
			input:   "//foo/bar.txt",
			destDir: "/foo/",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RebasePath(tc.input, tc.destDir)

			if tc.wantErr {
				if err == nil {
					t.Errorf("RebasePath(%q, %q) succeeded with %q, want error", tc.input, tc.destDir, got)
				}
				return
			}

			if err != nil {
				t.Errorf("RebasePath(%q, %q) returned error %v, want success", tc.input, tc.destDir, err)
				return
			}

			if got != tc.want {
				t.Errorf("RebasePath(%q, %q) = %q; want %q", tc.input, tc.destDir, got, tc.want)
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
