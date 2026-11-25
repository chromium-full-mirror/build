// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"os"
	"path/filepath"
	"testing"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/reapi/reapitest"
)

// TestBuild_DirSpecCacheCap checks that Options.DirSpecCacheBytes reaches
// the dirSpecs cache, and only in builds with two-phase caching.
func TestBuild_DirSpecCacheCap(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx := t.Context()
	dir := tempDir(t)
	for name, content := range map[string]string{
		"build/config/siso/main.star": `load("@builtin//struct.star", "module")

def init(ctx):
    return module(
        "config",
        step_config = "{}",
        filegroups = {},
        handlers = {},
    )
`,
		"out/siso/build.ninja": `rule r
  command = true
build out: r
`,
	} {
		fname := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(fname), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fname, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	client := reapitest.New(ctx, t, &reapitest.Fake{})
	const capBytes = 64 << 20
	for _, tc := range []struct {
		name       string
		experiment string
		want       int64
	}{
		{name: "no_two_phase_caching", want: 0},
		{name: "two_phase_caching", experiment: "two-phase-caching", want: capBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build.SetExperimentForTest(tc.experiment)
			t.Cleanup(func() { build.SetExperimentForTest("") })
			opt, graph, cleanup := setupBuild(ctx, t, dir, hashfs.Option{})
			defer cleanup()
			opt.REAPIClient = client
			opt.DirSpecCacheBytes = capBytes
			b, err := build.New(ctx, graph, opt)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if got := b.DirSpecCacheStats().CapBytes; got != tc.want {
				t.Errorf("DirSpecCacheStats().CapBytes=%d; want %d", got, tc.want)
			}
		})
	}
}
