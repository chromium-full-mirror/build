// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.chromium.org/build/siso/build/ninjabuild"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestNewServer_MissingWorkspace(t *testing.T) {
	tmp := tempDir(t)
	t.Chdir(tmp)

	cfg := ServerConfig{
		Version:          "test-version",
		LocalDevelopment: false,
		Port:             8080,
		OutDir: ninjabuild.DirFlag{
			Dir:           "non_existent_dir",
			ConfigRepoDir: "build/config/siso",
		},
		ManifestPath: "build.ninja",
	}

	_, err := NewServer(t.Context(), cfg)
	if err == nil {
		t.Fatal("NewServer succeeded; want error")
	}

	if _, ok := errors.AsType[*ErrWorkspaceNotExist](err); !ok {
		t.Fatalf("NewServer error: got %v; want ErrWorkspaceNotExist", err)
	}
}

func TestNewServer_Success(t *testing.T) {
	tmp := tempDir(t)
	t.Chdir(tmp)

	if err := os.MkdirAll("build/config/siso", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("out/Default", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("out/Default/build.ninja", []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := ServerConfig{
		Version:          "test-version",
		LocalDevelopment: false,
		Port:             8080,
		OutDir: ninjabuild.DirFlag{
			Dir:           "out/Default",
			ConfigRepoDir: "build/config/siso",
		},
		ManifestPath: "build.ninja",
	}

	s, err := NewServer(t.Context(), cfg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

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
