// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPath_FromRelative(t *testing.T) {
	dir := t.TempDir()
	absPath := filepath.Join(t.TempDir(), "test")
	path := NewPath(dir, "out/siso")
	for _, tc := range []struct {
		in   string
		want string
	}{
		{
			in:   "foo",
			want: "out/siso/foo",
		},
		{
			in:   "foo/bar",
			want: "out/siso/foo/bar",
		},
		{
			in:   "../../foo/bar",
			want: "foo/bar",
		},
		{
			in:   filepath.Join(dir, "foo/bar"),
			want: "foo/bar",
		},
		{
			in:   absPath,
			want: absPath,
		},
	} {
		got, err := path.FromRelative(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("path.FromRelative(%q)=%q, %v; want %q, nil", tc.in, got, err, tc.want)
		}
	}
}

func TestPath_FromRelative_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("These tests are Windows only")
	}

	dir := t.TempDir()
	absPath := filepath.Join(t.TempDir(), "test")
	path := NewPath(dir, "out\\siso")
	for _, tc := range []struct {
		in   string
		want string
	}{
		{
			in:   "foo",
			want: "out/siso/foo",
		},
		{
			in:   "foo\\bar",
			want: "out/siso/foo/bar",
		},
		{
			in:   "..\\..\\foo\\bar",
			want: "foo/bar",
		},
		{
			in:   filepath.Join(dir, "foo\\bar"),
			want: "foo/bar",
		},
		{
			in:   absPath,
			want: absPath,
		},
	} {
		got, err := path.FromRelative(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("path.FromRelative(%q)=%q, %v; want %q, nil", tc.in, got, err, tc.want)
		}
	}
}

// TestPath_MaybeToRelative_SlashSeparated pins the documented contract:
// results are slash-separated on every host. filepath.Rel returns
// OS-native separators, so on Windows a nested path must be converted
// back to slashes before it reaches ninja node lookups and deps
// recording, which key on slash paths.
func TestPath_MaybeToRelative_SlashSeparated(t *testing.T) {
	ctx := t.Context()
	p := NewPath("/workspace", "out/Default")
	got := p.MaybeToRelative(ctx, "out/Default/obj/base/foo.o")
	if want := "obj/base/foo.o"; got != want {
		t.Errorf("MaybeToRelative(out/Default/obj/base/foo.o) = %q, want %q", got, want)
	}
	if strings.Contains(got, `\`) {
		t.Errorf("MaybeToRelative returned OS-native separators: %q", got)
	}
}
