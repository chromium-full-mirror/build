// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/ninjabuild"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/reapi/reapitest"
)

func TestBuild_TwoPhaseCaching_RestatContent(t *testing.T) {
	if !runInSubProcess(t) {
		return
	}
	ctx := t.Context()
	dir := tempDir(t)

	build.SetExperimentForTest("two-phase-caching,two-phase-caching-local-action-cache-map")
	userCacheDir := tempDir(t)
	t.Setenv("XDG_CACHE_HOME", userCacheDir)
	t.Setenv("LocalAppData", userCacheDir)

	exists := func(fname string) error {
		_, err := os.Stat(filepath.Join(dir, "out/siso", fname))
		return err
	}

	content := []byte("output of foo.o\n")
	depContent := []byte("foo.o: ../../base/foo.cc ../../base/foo.h\n")
	fakere := &reapitest.Fake{
		ExecuteFunc: func(fakere *reapitest.Fake, action *rpb.Action) (*rpb.ActionResult, error) {
			dg, err := fakere.Put(ctx, content)
			if err != nil {
				return nil, err
			}
			depDg, err := fakere.Put(ctx, depContent)
			if err != nil {
				return nil, err
			}
			return &rpb.ActionResult{
				ExitCode: 0,
				OutputFiles: []*rpb.OutputFile{
					{
						Path:   "foo.o",
						Digest: dg,
					},
					{
						Path:   "foo.o.d",
						Digest: depDg,
					},
				},
			}, nil
		},
	}
	// keep global datasource to keep mock REAPI server.
	var ds build.DataSource
	defer func() {
		err := ds.Close(ctx)
		if err != nil {
			t.Error(err)
		}
	}()
	ds.Client = reapitest.New(ctx, t, fakere)
	err := ds.Client.Init(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ds.Cache = ds.Client.CacheStore()

	runNinja := func(ctx context.Context, t *testing.T, metricsBuffer *syncBuffer) (build.Stats, error) {
		t.Helper()
		hashfsOpts := hashfs.Option{
			StateFile:   ".siso_fs_state",
			OutputLocal: func(context.Context, string) bool { return true },
			DataSource:  ds,
		}
		opt, graph, cleanup := setupBuild(ctx, t, dir, hashfsOpts)
		defer cleanup()
		bcache, err := build.NewCache(ctx, build.CacheOptions{
			Store:      ds.Cache,
			EnableRead: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		opt.Cache = bcache
		opt.RECacheEnableRead = true
		opt.RECacheEnableWrite = true
		opt.REAPIClient = ds.Client
		opt.REExecEnable = true
		if metricsBuffer != nil {
			opt.MetricsJSONWriter = metricsBuffer
		}
		return ninjabuild.Run(ctx, graph, opt, []string{"all"}, ninjabuild.RunNinjaOpts{})
	}

	setupFiles(t, dir, t.Name(), nil)
	func() {
		t.Logf("--- first build")
		_, err := runNinja(ctx, t, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := exists("foo.o"); err != nil {
			t.Errorf("foo.o doesn't exist: %v", err)
		}
		if err := exists("bar.out"); err != nil {
			t.Errorf("bar.out doesn't exist: %v", err)
		}
	}()

	func() {
		t.Logf("--- second build: touch base/foo.h, expect two-phase cache hit for foo.o and bar.out skipped")
		touchFile(t, dir, "base/foo.h")
		var metricsBuffer syncBuffer
		stat, err := runNinja(ctx, t, &metricsBuffer)
		if err != nil {
			t.Fatal(err)
		}
		if stat.Skipped != 2 { // all(phony) and bar.out
			t.Errorf("Skipped=%d; want 2", stat.Skipped)
		}
		dec := json.NewDecoder(bytes.NewReader(metricsBuffer.buf.Bytes()))
		foundFoo := false
		for dec.More() {
			var m build.StepMetric
			err := dec.Decode(&m)
			if err != nil {
				t.Errorf("decode %v", err)
			}
			if m.StepID == "" {
				continue
			}
			switch filepath.Base(m.Output()) {
			case "foo.o":
				foundFoo = true
				if m.Err {
					t.Errorf("%s err=%t; want false", m.Output(), m.Err)
				}
				if !m.TwoPhaseCacheHit {
					t.Errorf("%s TwoPhaseCacheHit=%t; want true", m.Output(), m.TwoPhaseCacheHit)
				}
			default:
				t.Errorf("unexpected output %q: %#v", m.Output(), m)
			}
		}
		if !foundFoo {
			t.Errorf("foo.o not found in metrics")
		}
	}()

	func() {
		t.Logf("--- third build: update base/foo.h, expect two-phase cache miss and bar.out rebuilt")
		content = []byte("output of foo.o modified\n")
		modifyFile(t, dir, "base/foo.h", func(buf []byte) []byte {
			return append(buf, []byte("\n// modified")...)
		})
		var metricsBuffer syncBuffer
		stat, err := runNinja(ctx, t, &metricsBuffer)
		if err != nil {
			t.Fatal(err)
		}
		if stat.Skipped != 1 { // all(phony)
			t.Errorf("Skipped=%d; want 1", stat.Skipped)
		}
		dec := json.NewDecoder(bytes.NewReader(metricsBuffer.buf.Bytes()))
		built := make(map[string]bool)
		for dec.More() {
			var m build.StepMetric
			err := dec.Decode(&m)
			if err != nil {
				t.Errorf("decode %v", err)
			}
			if m.StepID == "" {
				continue
			}
			built[filepath.Base(m.Output())] = true
		}
		if !built["foo.o"] {
			t.Errorf("foo.o was not built")
		}
		if !built["bar.out"] {
			t.Errorf("bar.out was not built")
		}
	}()
}
