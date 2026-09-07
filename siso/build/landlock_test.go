// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/siso/execute"
	"go.chromium.org/build/siso/path"
)

func TestIsAncestor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("These tests only work on linux (windows absolute paths are different)")
	}
	for _, tc := range []struct {
		p1   string
		p2   string
		want bool
	}{
		{p1: "a", p2: "a/b", want: true},
		{p1: "a/b", p2: "a/b", want: true},
		{p1: "a", p2: "a/b/c/d", want: true},
		{p1: "a", p2: "ab", want: false},
		{p1: "a", p2: "a", want: true},
		{p1: "foo", p2: "fooo/bar", want: false},
		{p1: "a/b", p2: "a", want: false},
		{p1: "a/b", p2: "c/d", want: false},
		{p1: ".", p2: "c", want: true},
		{p1: "..", p2: ".", want: true},
		{p1: "..", p2: "a", want: true},
		{p1: "../foo", p2: "../foo/bar", want: true},
		{p1: "../foo", p2: "..", want: false},
		{p1: "/a", p2: "/a/b", want: true},
		{p1: "/a", p2: "/b", want: false},
		{p1: "/a", p2: "/a", want: true},
		{p1: "/", p2: "/a", want: true},
		{p1: "foo", p2: "/cwd/foo/bar", want: true},
		{p1: "foo", p2: "/cwd/bar", want: false},
		{p1: "foo", p2: "/cwd/foo", want: true},
	} {
		t.Run(fmt.Sprintf("%s_in_%s", tc.p1, tc.p2), func(t *testing.T) {
			got := isAncestor("/cwd", tc.p1, tc.p2)
			if got != tc.want {
				t.Errorf("isAncestor(%q, %q) = %v; want %v", tc.p1, tc.p2, got, tc.want)
			}
		})
	}
}

type fakeExecutor struct {
	req landlockRequest
}

func (f *fakeExecutor) Run(ctx context.Context, cmd *execute.Cmd) error {
	if len(cmd.Args) != 3 || cmd.Args[1] != "landlock" {
		return fmt.Errorf("unexpected command args: %v", cmd.Args)
	}
	data, err := os.ReadFile(cmd.Args[2])
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &f.req)
}

func TestLandlockExecutor_OutputDirs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Landlock is only supported on linux")
	}
	t.Setenv("TMPDIR", "")
	for _, tc := range []struct {
		name          string
		outputs       []string
		workspaceRoot string
		wantRWDirs    []string
	}{
		{
			name:       "exact duplicates",
			outputs:    []string{"out/gen/foo.h", "out/gen/bar.h"},
			wantRWDirs: []string{"out/gen", "/tmp", "/dev", "/proc"},
		},
		{
			name:       "ancestor first, descendant later",
			outputs:    []string{"out/gen/foo.h", "out/gen/sub/bar.h", "out/gen/sub/nested/baz.h"},
			wantRWDirs: []string{"out/gen", "/tmp", "/dev", "/proc"},
		},
		{
			name:       "descendant first, ancestor later",
			outputs:    []string{"out/gen/sub/bar.h", "out/gen/sub2/baz.h", "out/gen/foo.h"},
			wantRWDirs: []string{"out/gen", "/tmp", "/dev", "/proc"},
		},
		{
			name:       "deep hierarchy",
			outputs:    []string{"a/b/c/d/e.o", "a/b/c/d.o", "a/b.o"},
			wantRWDirs: []string{"a", "/tmp", "/dev", "/proc"},
		},
		{
			name:       "multiple independent trees",
			outputs:    []string{"tree1/sub1/a.o", "tree2/sub2/sub3/b.o", "tree1/c.o", "tree2/sub2/d.o"},
			wantRWDirs: []string{"tree1", "tree2/sub2", "/tmp", "/dev", "/proc"},
		},
		{
			name:          "workspace root under /tmp does not grant /tmp",
			outputs:       []string{"out/gen/foo.h"},
			workspaceRoot: "/tmp/my_test_workspace",
			wantRWDirs:    []string{"/tmp/my_test_workspace/out/gen", "/dev", "/proc"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeExecutor{}
			executor := newLandlockExecutor(fake, nil)
			cmd := &execute.Cmd{
				Outputs:       path.Paths(tc.outputs),
				WorkspaceRoot: tc.workspaceRoot,
			}
			ctx := t.Context()
			err := executor.Run(ctx, cmd)
			if err != nil {
				t.Fatalf("executor.Run: %v", err)
			}
			if diff := cmp.Diff(tc.wantRWDirs, fake.req.RWDirs); diff != "" {
				t.Errorf("RWDirs mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
