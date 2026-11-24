// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package buildconfig

import (
	"io/fs"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"go.chromium.org/build/siso/execute"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
)

func TestActionsWrite(t *testing.T) {
	ctx := t.Context()
	hfs, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := hfs.Close(ctx)
		if err != nil {
			t.Errorf("hfs.Close=%v", err)
		}
	}()
	cmd := &execute.Cmd{
		HashFS:        hfs,
		WorkspaceRoot: t.TempDir(),
	}
	write, err := starCmdActions(ctx, cmd).(*starlarkstruct.Struct).Attr("write")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		fname  string
		kwargs []starlark.Tuple
		want   fs.FileMode
	}{
		{
			fname: "default",
			want:  0644,
		},
		{
			fname:  "executable",
			kwargs: []starlark.Tuple{{starlark.String("is_executable"), starlark.True}},
			want:   0755,
		},
		{
			fname:  "not_executable",
			kwargs: []starlark.Tuple{{starlark.String("is_executable"), starlark.False}},
			want:   0644,
		},
	} {
		t.Run(tc.fname, func(t *testing.T) {
			thread := &starlark.Thread{Name: t.Name()}
			args := starlark.Tuple{starlark.String(tc.fname), starlark.Bytes("content")}
			_, err := starlark.Call(thread, write, args, tc.kwargs)
			if err != nil {
				t.Fatalf("write(%v, %v)=%v; want nil err", args, tc.kwargs, err)
			}
			fi, err := hfs.Stat(ctx, cmd.WorkspaceRoot, path.New(tc.fname))
			if err != nil {
				t.Fatal(err)
			}
			if got := fi.Mode().Perm(); got != tc.want {
				t.Errorf("mode=%v; want %v", got, tc.want)
			}
		})
	}
}
