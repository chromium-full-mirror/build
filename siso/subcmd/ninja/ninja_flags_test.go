// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninja

import (
	"flag"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// enableOfflineMode replaces the reapi options, but must keep the digest
// function: offline builds still hash and interpret .siso_fs_state under
// -reapi_digest_function, or a non-sha256 build's state would be discarded
// and re-hashed as sha256.
func TestEnableOfflineModeKeepsDigestFunction(t *testing.T) {
	c := &Command{}
	flagSet := flag.NewFlagSet("ninja", flag.ContinueOnError)
	c.SetFlags(flagSet)
	if err := flagSet.Parse([]string{"-reapi_digest_function", "blake3", "-offline"}); err != nil {
		t.Fatalf("flag parse %v; want nil err", err)
	}
	c.enableOfflineMode(t.Context())
	if got, want := c.reopt.DigestFunction, "blake3"; got != want {
		t.Errorf("after enableOfflineMode, reopt.DigestFunction = %q, want %q", got, want)
	}
}

func TestParseFlagsFully(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		want      []string
		wantDebug debugMode
	}{
		{
			name: "simple",
			args: []string{"-C", "out/siso"},
			want: nil,
		},
		{
			name: "target",
			args: []string{"-C", "out/siso", "-project", "rbe-chrome-untrusted", "chrome"},
			want: []string{"chrome"},
		},
		{
			name: "after-flag",
			args: []string{"-C", "out/siso", "-project", "rbe-chrome-untrusted", "chrome", "-d", "explain"},
			want: []string{"chrome"},
			wantDebug: debugMode{
				Explain: true,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Command{}
			flagSet := flag.NewFlagSet("ninja", flag.ContinueOnError)
			c.SetFlags(flagSet)
			err := flagSet.Parse(tc.args)
			if err != nil {
				t.Fatalf("flag parse %v; want nil err", err)
			}
			err = parseFlagsFully(flagSet)
			if err != nil {
				t.Fatalf("flag parse fully %v; want nil err", err)
			}
			if diff := cmp.Diff(tc.want, flagSet.Args()); diff != "" {
				t.Errorf("args diff -want +got:\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantDebug, c.debugMode); diff != "" {
				t.Errorf("debugMode diff -want +got:\n%s", diff)
			}
		})
	}
}

func TestMissingDepsFlag(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantMode string
		wantErr  bool
	}{
		{
			name:     "default",
			args:     []string{"-C", "out/siso"},
			wantMode: "ignore",
		},
		{
			name:     "single_dash_warn",
			args:     []string{"-C", "out/siso", "-missing_deps=warn"},
			wantMode: "warn",
		},
		{
			name:     "double_dash_error",
			args:     []string{"-C", "out/siso", "--missing_deps=error"},
			wantMode: "error",
		},
		{
			name:     "ignore",
			args:     []string{"-C", "out/siso", "-missing_deps=ignore"},
			wantMode: "ignore",
		},
		{
			name:     "fatal",
			args:     []string{"-C", "out/siso", "-missing_deps=fatal"},
			wantMode: "fatal",
		},
		{
			name:    "empty",
			args:    []string{"-C", "out/siso", "-missing_deps="},
			wantErr: true,
		},
		{
			name:    "invalid",
			args:    []string{"-C", "out/siso", "-missing_deps=invalid"},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Command{}
			flagSet := flag.NewFlagSet("ninja", flag.ContinueOnError)
			c.SetFlags(flagSet)
			err := flagSet.Parse(tc.args)
			if err != nil {
				t.Fatalf("flag parse: %v", err)
			}
			err = c.resolveFlags()
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("c.resolveFlags() = %v; gotErr %t, wantErr %t", err, gotErr, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if c.missingDeps != tc.wantMode {
				t.Errorf("c.missingDeps = %q, want %q", c.missingDeps, tc.wantMode)
			}
		})
	}
}

func TestCleandeadFlag(t *testing.T) {
	for _, tc := range []struct {
		name          string
		args          []string
		wantCleandead bool
		wantSubtool   string
	}{
		{
			name:          "default",
			args:          []string{"-C", "out/siso"},
			wantCleandead: false,
			wantSubtool:   "",
		},
		{
			name:          "cleandead_flag",
			args:          []string{"-C", "out/siso", "-cleandead"},
			wantCleandead: true,
			wantSubtool:   "",
		},
		{
			name:          "subtool_cleandead",
			args:          []string{"-C", "out/siso", "-t", "cleandead"},
			wantCleandead: true,
			wantSubtool:   "cleandead",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Command{}
			flagSet := flag.NewFlagSet("ninja", flag.ContinueOnError)
			c.SetFlags(flagSet)
			if err := flagSet.Parse(tc.args); err != nil {
				t.Fatalf("flag parse: %v", err)
			}
			if err := c.resolveFlags(); err != nil {
				t.Fatalf("resolveFlags: %v", err)
			}
			if c.cleandead != tc.wantCleandead {
				t.Errorf("c.cleandead = %t, want %t", c.cleandead, tc.wantCleandead)
			}
			if c.subtool != tc.wantSubtool {
				t.Errorf("c.subtool = %q, want %q", c.subtool, tc.wantSubtool)
			}
		})
	}
}

func TestSetupOutputLocal_CleandeadPreservesNonLocal(t *testing.T) {
	ctx := t.Context()
	c := &Command{}
	flagSet := flag.NewFlagSet("ninja", flag.ContinueOnError)
	c.SetFlags(flagSet)
	if err := flagSet.Parse([]string{"-C", "out/siso", "-t", "cleandead"}); err != nil {
		t.Fatalf("flag parse: %v", err)
	}
	if err := c.resolveFlags(); err != nil {
		t.Fatalf("resolveFlags: %v", err)
	}
	if err := c.setupOutputLocal(ctx); err != nil {
		t.Fatalf("setupOutputLocal: %v", err)
	}
	if c.outputLocal(ctx, "clang_x64/obj/third_party/libwebp/cwebp/cwebp.o") {
		t.Errorf("c.outputLocal for cleandead returned true; want false so remote-only entries in .siso_fs_state are preserved")
	}
}
