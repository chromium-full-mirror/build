// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package shutil

import "testing"

func TestStripEnvPrefix(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cmdline string
		want    string
	}{
		{
			name:    "no-env",
			cmdline: "clang -c foo.c",
			want:    "clang -c foo.c",
		},
		{
			name:    "single-inline",
			cmdline: "PWD=/proc/self/cwd clang -c foo.c",
			want:    " clang -c foo.c",
		},
		{
			name:    "multiple-inline",
			cmdline: "FOO=1 BAR=2 clang -c foo.c",
			want:    " clang -c foo.c",
		},
		{
			name:    "inline-empty-value",
			cmdline: "FOO= clang -c foo.c",
			want:    " clang -c foo.c",
		},
		{
			name:    "inline-with-and",
			cmdline: "PWD=/proc/self/cwd && clang -c foo.c",
			want:    " clang -c foo.c",
		},
		{
			name:    "inline-with-semicolon",
			cmdline: "PWD=/proc/self/cwd; clang -c foo.c",
			want:    " clang -c foo.c",
		},
		{
			name:    "export-with-and",
			cmdline: "export LD_LIBRARY_PATH=/foo/bar && clang -c foo.c",
			want:    " clang -c foo.c",
		},
		{
			name:    "regular-and-export-with-and",
			cmdline: "PWD=/proc/self/cwd && export LD_LIBRARY_PATH=/foo/bar && PWD=/proc/self/cwd clang -c foo.c",
			want:    " clang -c foo.c",
		},
		{
			name:    "export-multiple-vars-and-bare-ident",
			cmdline: "export FOO=1 BAR BAZ=2 && clang -c foo.c",
			want:    " clang -c foo.c",
		},
		{
			name:    "export-with-semicolon",
			cmdline: "export FOO=1; clang -c foo.c",
			want:    " clang -c foo.c",
		},
		{
			name:    "mixed-inline-and-export",
			cmdline: "PWD=/proc/self/cwd && export LD_LIBRARY_PATH=/a:/b && PWD=/proc/self/cwd clang -c foo.c",
			want:    " clang -c foo.c",
		},
		{
			name:    "quoted-and-escaped-values",
			cmdline: `export FOO="a && b" BAR='c;d' BAZ="e\"f" && X=1\ 2 clang -c foo.c`,
			want:    " clang -c foo.c",
		},
		{
			name:    "command-path-with-equals",
			cmdline: "path/with=eq/clang -DFOO=bar -c foo.c",
			want:    "path/with=eq/clang -DFOO=bar -c foo.c",
		},
		{
			name:    "export-without-separator",
			cmdline: "export FOO=1 clang",
			want:    "export FOO=1 clang",
		},
		{
			name:    "unclosed-quote",
			cmdline: `FOO="unclosed clang -c foo.c`,
			want:    `FOO="unclosed clang -c foo.c`,
		},
		{
			name:    "command-substitution-dollar-parens",
			cmdline: "FOO=$(cat a) clang -c foo.c",
			want:    "FOO=$(cat a) clang -c foo.c",
		},
		{
			name:    "command-substitution-backticks",
			cmdline: "FOO=`cat a` clang -c foo.c",
			want:    "FOO=`cat a` clang -c foo.c",
		},
		{
			name:    "unquoted-dollar-var",
			cmdline: "FOO=$BAR clang -c foo.c",
			want:    "FOO=$BAR clang -c foo.c",
		},
		{
			name:    "unquoted-parens-array",
			cmdline: "FOO=(1 2) clang -c foo.c",
			want:    "FOO=(1 2) clang -c foo.c",
		},
		{
			name:    "subshell-parens",
			cmdline: "(FOO=1 clang -c foo.c)",
			want:    "(FOO=1 clang -c foo.c)",
		},
		{
			name:    "redirection-out",
			cmdline: "FOO=bar>out clang -c foo.c",
			want:    "FOO=bar>out clang -c foo.c",
		},
		{
			name:    "redirection-in",
			cmdline: "FOO=bar<in clang -c foo.c",
			want:    "FOO=bar<in clang -c foo.c",
		},
		{
			name:    "pipe",
			cmdline: "FOO=bar|clang -c foo.c",
			want:    "FOO=bar|clang -c foo.c",
		},
		{
			name:    "quoted-shell-metachars",
			cmdline: "FOO=\"$BAR\" BAR='$(cat a)' BAZ=\"a|b>c<d(e)`f`\" clang -c foo.c",
			want:    " clang -c foo.c",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := StripEnvPrefix(tc.cmdline)
			if got != tc.want {
				t.Errorf("stripEnvPrefix(%q) = %q, want %q", tc.cmdline, got, tc.want)
			}
		})
	}
}
