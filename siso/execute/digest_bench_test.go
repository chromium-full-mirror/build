// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execute

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
)

// newDigestTestCmd creates n input files (in a few directories, and one
// symlink) under a new workspace and returns a Cmd using them.
func newDigestTestCmd(tb testing.TB, n int) *Cmd {
	tb.Helper()
	if runtime.GOOS == "windows" {
		tb.Skip("symlink not available on windows")
	}
	ctx := tb.Context()
	dir, err := filepath.EvalSymlinks(tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
	var inputs []path.Path
	for i := range n {
		name := fmt.Sprintf("src/dir%d/f%d.h", i%16, i)
		if i%3 == 0 {
			name = fmt.Sprintf("out/siso/gen/dir%d/g%d.h", i%7, i)
		}
		fname := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(fname, []byte(name), 0644); err != nil {
			tb.Fatal(err)
		}
		inputs = append(inputs, path.Path(name))
	}
	if err := os.Symlink("dir1", filepath.Join(dir, "src/link")); err != nil {
		tb.Fatal(err)
	}
	inputs = append(inputs, "src/link")
	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { hashFS.Close(ctx) })
	if err := hashFS.WaitReady(ctx); err != nil {
		tb.Fatal(err)
	}
	return &Cmd{
		ID:            "digest-test",
		Args:          []string{"clang++", "-c", "../../src/dir1/f1.h"},
		WorkspaceRoot: dir,
		WorkDir:       "out/siso",
		Pure:          true,
		HashFS:        hashFS,
		Inputs:        inputs,
		Outputs:       []path.Path{"out/siso/obj/x.o"},
	}
}

// TestCmdDigestGolden pins the action digests of representative commands,
// so that changes to inputTree can't change action digests or 2PC keys
// unnoticed. (Remote chroot digests depend on the workspace root, so they
// can't be pinned here.)
func TestCmdDigestGolden(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Cmd)
		want  string
	}{
		{
			name:  "plain",
			setup: func(*Cmd) {},
			want:  "30537a24feff94442225c9d9bfc4daf85347813d53d521b6344e2530543c28b4/141",
		},
		{
			name: "canonicalize",
			setup: func(c *Cmd) {
				c.CanonicalizeDir = true
			},
			want: "cba245ffdfeea932c1ef0bfc3a25677c95181dccbf200963bf6687b842b570e4/141",
		},
		{
			name: "remote_inputs",
			setup: func(c *Cmd) {
				c.RemoteInputs = map[path.Path]path.Path{
					"remote/f1.h":   "src/dir1/f1.h",
					"src/dir2/f2.h": "src/dir3/f19.h",
				}
			},
			want: "2ed62a65d60f8bb90527b601ea21413d8eba6989a8798553a00b814f319a46fb/141",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newDigestTestCmd(t, 300)
			tc.setup(c)
			d, err := c.Digest(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if d.String() != tc.want {
				t.Errorf("Digest=%s; want %s", d, tc.want)
			}
		})
	}
}

// BenchmarkCmdDigest computes the action digest of a command with 3000
// inputs, as for the 2PC lookup key of each step.
func BenchmarkCmdDigest(b *testing.B) {
	ctx := b.Context()
	c := newDigestTestCmd(b, 3000)
	b.ReportAllocs()
	for b.Loop() {
		c.actionDigest = digest.Digest{}
		_, err := c.Digest(ctx, nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCmdDigestAfterLargeStep computes action digests after a
// large step has put its buffer into the pool.
func BenchmarkCmdDigestAfterLargeStep(b *testing.B) {
	for _, n := range []int{300, 3000} {
		for _, seeded := range []bool{false, true} {
			b.Run(fmt.Sprintf("inputs=%d/after64Ki=%t", n, seeded), func(b *testing.B) {
				c := newDigestTestCmd(b, n)
				for i := range entriesPools {
					for range 4 {
						entriesPools[i].Get() // start with empty pools
					}
				}
				if seeded {
					putEntriesBuf(getEntriesBuf(40000))
				}
				b.ReportAllocs()
				for b.Loop() {
					c.actionDigest = digest.Digest{}
					if _, err := c.Digest(b.Context(), nil); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
