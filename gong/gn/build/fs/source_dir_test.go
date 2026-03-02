// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package fs

import (
	"runtime"
	"testing"
)

func TestMakeSourceDir(t *testing.T) {
	for _, tc := range []struct {
		path    string
		want    string
		wantErr bool
	}{
		{
			path: "//foo/bar",
			want: "//foo/bar/",
		},
		{
			path: "//foo/bar/",
			want: "//foo/bar/",
		},
		{
			path: "//",
			want: "//",
		},
		{
			path: "/",
			want: "/",
		},
		{
			path:    "relative/path",
			wantErr: true,
		},
	} {
		t.Run(tc.path, func(t *testing.T) {
			d, err := MakeSourceDir(tc.path)
			if err != nil {
				if !tc.wantErr {
					t.Errorf("MakeSourceDir(%q) failed: %v", tc.path, err)
				}
				return
			}
			if tc.wantErr {
				t.Errorf("MakeSourceDir(%q) succeeded; want error", tc.path)
				return
			}
			if got := d.Path(); got != tc.want {
				t.Errorf("MakeSourceDir(%q) = %q; want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestResolveRelativeFile(t *testing.T) {
	dir, _ := MakeSourceDir("//source/dir/")

	for _, tc := range []struct {
		input   string
		want    string
		wantErr bool
	}{
		{
			input: "file.txt",
			want:  "//source/dir/file.txt",
		},
		{
			input: "../file.txt",
			want:  "//source/file.txt",
		},
		{
			input: "//abs/file.txt",
			want:  "//abs/file.txt",
		},
		{
			input: "/sys/file.txt",
			want:  "/sys/file.txt",
		},
		{
			input:   "/sys/dir/",
			wantErr: true,
		},
		{
			input:   "",
			wantErr: true,
		},
	} {
		t.Run(tc.input, func(t *testing.T) {
			f, err := dir.ResolveRelativeFile(tc.input)
			if err != nil {
				if !tc.wantErr {
					t.Errorf("ResolveRelativeFile(%q) failed: %v", tc.input, err)
				}
				return
			}
			if tc.wantErr {
				t.Errorf("ResolveRelativeFile(%q) succeeded; want error", tc.input)
				return
			}
			if got := f.Filename(); got != tc.want {
				t.Errorf("ResolveRelativeFile(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestResolveRelativeDir(t *testing.T) {
	dir, _ := MakeSourceDir("//base/")

	for _, tc := range []struct {
		input   string
		want    string
		wantErr bool
	}{
		{
			input: "foo",
			want:  "//base/foo/",
		},
		{
			input: "../foo",
			want:  "//foo/",
		},
		{
			input: "//abs/foo",
			want:  "//abs/foo/",
		},
		{
			input: "/sys/foo",
			want:  "/sys/foo/",
		},
		{
			input: "/sys/looks-like-file.txt",
			want:  "/sys/looks-like-file.txt/",
		},
		{
			input:   "",
			wantErr: true,
		},
	} {
		t.Run(tc.input, func(t *testing.T) {
			f, err := dir.ResolveRelativeDir(tc.input)
			if err != nil {
				if !tc.wantErr {
					t.Errorf("ResolveRelativeDir(%q) failed: %v", tc.input, err)
				}
				return
			}
			if tc.wantErr {
				t.Errorf("ResolveRelativeDir(%q) succeeded; want error", tc.input)
				return
			}
			if got := f.Path(); got != tc.want {
				t.Errorf("ResolveRelativeDir(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestMakeSourceDirFromPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("See TestMakeSourceDirFromPath_Windows instead")
	}

	for _, tc := range []struct {
		name string
		root string
		path string
		want string
	}{
		{name: "outside", root: "/source/foo/", path: "/foo/bar/", want: "/foo/bar/"},
		{name: "systemroot", root: "/source/foo/", path: "/", want: "/"},
		{name: "buildroot", root: "/source/foo/", path: "/source/foo/", want: "//"},
		{name: "buildrootnoslash", root: "/source/foo/", path: "/source/foo", want: "//"},
		{name: "subdir", root: "/source/foo/", path: "/source/foo/bar/", want: "//bar/"},
		{name: "subdirnested", root: "/source/foo/", path: "/source/foo/bar/baz/", want: "//bar/baz/"},
		{name: "casesensitive", root: "/source/foo/", path: "/SOURCE/foo/bar/", want: "/SOURCE/foo/bar/"},
		{name: "noroot", root: "", path: "/source/foo", want: "/source/foo/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MakeSourceDirFromPath(tc.root, tc.path)
			if err != nil {
				t.Fatalf("MakeSourceDirFromPath(%q, %q) failed: %v", tc.root, tc.path, err)
			}
			if got.Path() != tc.want {
				t.Errorf("MakeSourceDirFromPath(%q, %q) = %q; want %q", tc.root, tc.path, got.Path(), tc.want)
			}
		})
	}
}

func TestMakeSourceDirFromPath_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("See TestMakeSourceDirFromPath instead")
	}

	for _, tc := range []struct {
		name string
		root string
		path string
		want string
	}{
		{name: "outside", root: `C:\source\foo\`, path: `C:\foo\bar`, want: `/C:/foo/bar/`},
		{name: "normalize", root: `C:\source\foo\`, path: `C:foo/bar/`, want: `/C:/foo/bar/`},
		{name: "ignoreunix", root: `C:\source\foo\`, path: `/`, want: `/`},
		{name: "ignoreunix2", root: `C:\source\foo\`, path: `/foo/bar/`, want: `/foo/bar/`},
		{name: "buildroot", root: `C:\source\foo\`, path: `C:\source\foo\`, want: `//`},
		{name: "buildrootnoslash", root: `C:\source\foo\`, path: `C:\source\foo`, want: `//`},
		{name: "subdir", root: `C:\source\foo\`, path: `C:\source\foo\bar\`, want: `//bar/`},
		{name: "subdirnested", root: `C:\source\foo\`, path: `C:\source\foo\bar\baz`, want: `//bar/baz/`},
		{name: "caseinsensitive", root: `C:\source\foo\`, path: `c:/SOURCE\Foo/baR/`, want: `//baR/`},
		{name: "noroot", root: "", path: `C:\source\foo`, want: `/C:/source/foo/`},
		// Also allow absolute GN-style Windows paths.
		{name: "outside-gnstyle", root: `C:\source\foo\`, path: `/C:/foo/bar`, want: `/C:/foo/bar/`},
		{name: "subdir-gnstyle", root: `C:\source\foo\`, path: `/C:/source/foo/bar`, want: `//bar/`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MakeSourceDirFromPath(tc.root, tc.path)
			if err != nil {
				t.Fatalf("MakeSourceDirFromPath(%q, %q) failed: %v", tc.root, tc.path, err)
			}
			if got.Path() != tc.want {
				t.Errorf("MakeSourceDirFromPath(%q, %q) = %q; want %q", tc.root, tc.path, got.Path(), tc.want)
			}
		})
	}
}
