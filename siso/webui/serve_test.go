// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"go.chromium.org/build/siso/build/ninjabuild"
)

func TestServer_MissingWorkspace(t *testing.T) {
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	_, err = NewServer(t.Context(), ServerConfig{
		Version:          "test-version",
		LocalDevelopment: false,
		Port:             8080,
		OutDir: ninjabuild.DirFlag{
			Dir:           "non_existent_dir",
			ConfigRepoDir: "build/config/siso",
		},
		ManifestPath: "build.ninja",
	})

	if err == nil {
		t.Fatal("server got err = nil; want error")
	}
	if _, ok := errors.AsType[*ErrWorkspaceNotExist](err); !ok {
		t.Fatalf("server got err = %v; want ErrWorkspaceNotExist", err)
	}
}

func mustServer(ctx context.Context, t *testing.T, cfg ServerConfig) (*WebuiServer, string) {
	t.Helper()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err := os.MkdirAll("build/config/siso", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("out/Default", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("out/Default/build.ninja", []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("out/Default/siso_metrics.json", []byte(`{"build_id": "test-rev"}
{"step_id": "step-1", "rule": "cc", "action": "clang", "outputs": ["out1.o"]}
`), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(ctx, cfg)
	if err != nil {
		t.Fatalf("server err = %v; want nil err", err)
	}
	return s, dir
}

func TestServer_InitialState(t *testing.T) {
	s, tmp := mustServer(t.Context(), t, ServerConfig{
		Version:          "test-version",
		LocalDevelopment: false,
		Port:             8080,
		OutDir: ninjabuild.DirFlag{
			Dir:           "out/Default",
			ConfigRepoDir: "build/config/siso",
		},
		ManifestPath: "build.ninja",
	})

	if s.workspaceRoot != tmp {
		t.Errorf("workspaceRoot = %q; want %q", s.workspaceRoot, tmp)
	}
	if s.defaultOutdir != "out/Default" {
		t.Errorf("defaultOutdir = %q; want %q", s.defaultOutdir, "out/Default")
	}
	if s.defaultOutdirRoot != "out" {
		t.Errorf("defaultOutdirRoot = %q; want %q", s.defaultOutdirRoot, "out")
	}
	if s.defaultOutdirSub != "Default" {
		t.Errorf("defaultOutdirSub = %q; want %q", s.defaultOutdirSub, "Default")
	}
}

func TestRoutes(t *testing.T) {
	s, _ := mustServer(t.Context(), t, ServerConfig{
		Version:          "test-version",
		LocalDevelopment: false,
		Port:             8080,
		OutDir: ninjabuild.DirFlag{
			Dir:           "out/Default",
			ConfigRepoDir: "build/config/siso",
		},
		ManifestPath: "build.ninja",
	})

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/out/Default/builds/test-rev/steps/", http.StatusOK},
		{"/out/Default/builds/test-rev/steps/step-1/", http.StatusOK},
		{"/out/Default/builds/test-rev/steps/step-0/", http.StatusNotFound},
		{"/out/Default/builds/nonexistent-rev/steps/step-1/", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		s.mux().ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("GET %s = %d; want %d", tc.path, rec.Code, tc.want)
		}
	}
}
