// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package nsjailutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"go.chromium.org/build/siso/hashfs"
)

// TestRenameFromJail verifies renameFromJail captures an output out of the nsjail exec root, replacing a non-empty pre-created destination cleanly (a plain os.Rename of a dir onto a non-empty dir fails ENOTEMPTY).
func TestRenameFromJail(t *testing.T) {
	t.Run("file_overwrites_destination", func(t *testing.T) {
		dir := t.TempDir()
		jail := filepath.Join(dir, "jail")
		dst := filepath.Join(dir, "dst")
		if err := os.MkdirAll(jail, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jail, "out.txt"), []byte("NEW"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dst, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, "out.txt"), []byte("OLD"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := renameFromJail(filepath.Join(jail, "out.txt"), filepath.Join(dst, "out.txt")); err != nil {
			t.Fatalf("renameFromJail(file): %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dst, "out.txt"))
		if err != nil || string(got) != "NEW" {
			t.Fatalf("dst content = %q, %v; want NEW", got, err)
		}
	})

	t.Run("dir_onto_populated_destination", func(t *testing.T) {
		dir := t.TempDir()
		jail := filepath.Join(dir, "jail", "gen")
		dst := filepath.Join(dir, "out", "gen")
		if err := os.MkdirAll(jail, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jail, "new.txt"), []byte("NEW"), 0644); err != nil {
			t.Fatal(err)
		}
		// Destination pre-created and populated by a prior build.
		if err := os.MkdirAll(dst, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, "stale.txt"), []byte("OLD"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := renameFromJail(jail, dst); err != nil {
			t.Fatalf("renameFromJail(dir onto populated dir): %v", err)
		}
		if _, err := os.Stat(filepath.Join(dst, "new.txt")); err != nil {
			t.Errorf("captured dir missing new.txt: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dst, "stale.txt")); !os.IsNotExist(err) {
			t.Errorf("stale file from previous build survived capture: err=%v", err)
		}
	})
}

func TestNew_PublicAndWritableDirs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("NSJail is only supported on linux")
	}
	t.Setenv("SISO_NSJAIL_PUBLIC_DIRS", "")
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	jailRoot := filepath.Join(dir, "jailroot")
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(jailRoot, 0755); err != nil {
		t.Fatal(err)
	}

	t.Run("default_mounts_when_unset", func(t *testing.T) {
		req := Request{
			ExePath:       "/bin/true",
			JailRootDir:   jailRoot,
			WorkspaceRoot: ws,
			WorkDir:       "out",
			OutDir:        "out",
		}
		jail, err := New(t.Context(), os.DirFS("/"), req)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer jail.Close()

		foundBin := false
		for _, m := range jail.config.Mount {
			if m.GetDst() == "/bin" && !m.GetRw() {
				foundBin = true
			}
		}
		if !foundBin {
			t.Errorf("expected /bin read-only mount by default, got mounts: %v", jail.config.Mount)
		}
	})

	t.Run("custom_public_and_writable_dirs", func(t *testing.T) {
		req := Request{
			ExePath:       "/bin/true",
			JailRootDir:   jailRoot,
			WorkspaceRoot: ws,
			WorkDir:       "out",
			OutDir:        "out",
			PublicDirs:    []string{"/custom/ro"},
			WritableDirs:  []string{"/custom/rw"},
		}
		jail, err := New(t.Context(), os.DirFS("/"), req)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer jail.Close()

		var foundRO, foundRW, foundDefaultBin bool
		for _, m := range jail.config.Mount {
			switch m.GetDst() {
			case "/custom/ro":
				foundRO = !m.GetRw()
			case "/custom/rw":
				foundRW = m.GetRw()
			case "/bin":
				foundDefaultBin = true
			}
		}
		if !foundRO {
			t.Errorf("expected /custom/ro mount with rw=false, got mounts: %v", jail.config.Mount)
		}
		if !foundRW {
			t.Errorf("expected /custom/rw mount with rw=true, got mounts: %v", jail.config.Mount)
		}
		if foundDefaultBin {
			t.Errorf("did not expect default /bin mount when custom dirs provided, got mounts: %v", jail.config.Mount)
		}
	})
}

func TestNew_InputsDeduplicationAndSymlinkDir(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("NSJail is only supported on linux")
	}
	t.Setenv("SISO_NSJAIL_PUBLIC_DIRS", "")
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	jailRoot := filepath.Join(dir, "jailroot")
	if err := os.MkdirAll(jailRoot, 0755); err != nil {
		t.Fatal(err)
	}

	writeFile := func(rel, content string) {
		t.Helper()
		p := filepath.Join(ws, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	makeSymlink := func(rel, target string) {
		t.Helper()
		p := filepath.Join(ws, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}

	// Setup workspace layout matching Android's symlinked include directories (b/579246632):
	// - frameworks/native/libs/gui/include/gui/AdditionalOptions.h (regular file)
	// - frameworks/native/include/gui -> ../libs/gui/include/gui (directory symlink)
	// - system/libbase/include/android-base/errors.h (regular file)
	// - system/core/libutils/include/utils/Errors.h -> ../../../../../system/libbase/include/android-base/errors.h (file symlink)
	// - system/core/include/utils -> ../libutils/include/utils/ (directory symlink)
	// - nested symlink directories: a/sym1 -> ../b/real, b/real/sub -> ../../other, other/foo.h
	writeFile("frameworks/native/libs/gui/include/gui/AdditionalOptions.h", "#pragma once\n")
	makeSymlink("frameworks/native/include/gui", "../libs/gui/include/gui")
	writeFile("system/libbase/include/android-base/errors.h", "#pragma once\n")
	makeSymlink("system/core/libutils/include/utils/Errors.h", "../../../../../system/libbase/include/android-base/errors.h")
	makeSymlink("system/core/include/utils", "../libutils/include/utils/")
	writeFile("other/foo.h", "#pragma once\n")
	makeSymlink("b/real/sub", "../../other")
	makeSymlink("a/sym1", "../b/real")
	makeSymlink("b/chain2", "../other")
	makeSymlink("a/chain1", "../b/chain2")

	outsideDir := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "foo.h"), []byte("#pragma once\n"), 0644); err != nil {
		t.Fatal(err)
	}
	makeSymlink("a/abs_sym", outsideDir)
	makeSymlink("a/escape_sym", "../../outside")
	makeSymlink("a/dangling_sym", "nonexistent_target")
	makeSymlink("a/loop1", "../a/loop2")
	makeSymlink("a/loop2", "../a/loop1")

	hfs, err := hashfs.New(t.Context(), hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := hfs.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})

	newWithInputs := func(inputs []string) (*NSJail, error) {
		req := Request{
			ExePath:       "/bin/true",
			JailRootDir:   jailRoot,
			WorkspaceRoot: ws,
			WorkDir:       "out",
			OutDir:        "out",
			PublicDirs:    []string{"/bin"},
			Inputs:        inputs,
		}
		return New(t.Context(), hfs.FileSystem(t.Context(), "/"), req)
	}

	type mount struct {
		src, dst                 string
		isBind, isSymlink, isDir bool
	}

	for _, tc := range []struct {
		name   string
		inputs []string
		want   []mount
	}{
		{
			name: "deduplicate_exact_files_and_symlinks",
			inputs: []string{
				"",
				".",
				"frameworks/native/include/gui",
				"frameworks/native/include/./gui",
				"system/libbase/include/android-base/errors.h",
				"system/libbase/include/android-base/errors.h",
			},
			want: []mount{
				{src: "../libs/gui/include/gui", dst: "/src/frameworks/native/include/gui", isSymlink: true},
				{src: filepath.Join(ws, "system/libbase/include/android-base/errors.h"), dst: "/src/system/libbase/include/android-base/errors.h", isBind: true},
			},
		},
		{
			name: "relocated_child_file_and_symlink_dir_b579246632",
			inputs: []string{
				"frameworks/native/libs/gui/include/gui/AdditionalOptions.h",
				"frameworks/native/include/gui",
				"frameworks/native/include/gui",
			},
			want: []mount{
				{src: filepath.Join(ws, "frameworks/native/libs/gui/include/gui/AdditionalOptions.h"), dst: "/src/frameworks/native/libs/gui/include/gui/AdditionalOptions.h", isBind: true},
				{src: "../libs/gui/include/gui", dst: "/src/frameworks/native/include/gui", isSymlink: true},
			},
		},
		{
			name: "symlink_dir_and_symlink_file_deduped",
			inputs: []string{
				"system/core/include/utils",
				"system/core/libutils/include/utils/Errors.h",
				"system/core/include/utils",
				"system/core/libutils/include/utils/Errors.h",
				"system/libbase/include/android-base/errors.h",
			},
			want: []mount{
				{src: "../libutils/include/utils/", dst: "/src/system/core/include/utils", isSymlink: true},
				{src: "../../../../../system/libbase/include/android-base/errors.h", dst: "/src/system/core/libutils/include/utils/Errors.h", isSymlink: true},
				{src: filepath.Join(ws, "system/libbase/include/android-base/errors.h"), dst: "/src/system/libbase/include/android-base/errors.h", isBind: true},
			},
		},
		{
			name: "real_dir_shadows_child_files_and_symlinks",
			inputs: []string{
				"frameworks/native/libs/gui/include/gui/AdditionalOptions.h",
				"frameworks/native/include/gui",
				"frameworks/native/libs/gui/include/gui",
				"system/core/include/utils",
				"system/core/libutils/include/utils/Errors.h",
				"system/core/libutils/include/utils",
				"system/libbase/include/android-base/errors.h",
			},
			want: []mount{
				{src: "../libs/gui/include/gui", dst: "/src/frameworks/native/include/gui", isSymlink: true},
				{src: filepath.Join(ws, "frameworks/native/libs/gui/include/gui"), dst: "/src/frameworks/native/libs/gui/include/gui", isBind: true, isDir: true},
				{src: "../libutils/include/utils/", dst: "/src/system/core/include/utils", isSymlink: true},
				{src: filepath.Join(ws, "system/core/libutils/include/utils"), dst: "/src/system/core/libutils/include/utils", isBind: true, isDir: true},
				{src: filepath.Join(ws, "system/libbase/include/android-base/errors.h"), dst: "/src/system/libbase/include/android-base/errors.h", isBind: true},
			},
		},
		{
			name: "real_dir_shadows_child_symlink_resolving_outside_dir",
			inputs: []string{
				"b/real",
				"other/foo.h",
				"b/real/sub",
			},
			want: []mount{
				{src: filepath.Join(ws, "b/real"), dst: "/src/b/real", isBind: true, isDir: true},
				{src: filepath.Join(ws, "other/foo.h"), dst: "/src/other/foo.h", isBind: true},
			},
		},
		{
			name: "leaf_symlinks_with_abs_nonlocal_or_dangling_target",
			inputs: []string{
				"a/abs_sym",
				"a/escape_sym",
				"a/dangling_sym",
			},
			want: []mount{
				{src: outsideDir, dst: "/src/a/abs_sym", isSymlink: true},
				{src: "../../outside", dst: "/src/a/escape_sym", isSymlink: true},
				{src: "nonexistent_target", dst: "/src/a/dangling_sym", isSymlink: true},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jail, err := newWithInputs(tc.inputs)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			// Strip standard prefix mounts (/bin, /tmp, /src, /src/out).
			var got []mount
			for _, m := range jail.config.Mount {
				switch m.GetDst() {
				case "/bin", "/tmp", "/src", "/src/out":
					continue
				default:
					got = append(got, mount{
						src:       m.GetSrc(),
						dst:       m.GetDst(),
						isBind:    m.GetIsBind(),
						isSymlink: m.GetIsSymlink(),
						isDir:     m.GetIsDir(),
					})
				}
			}
			jail.Close()
			if !slices.Equal(got, tc.want) {
				t.Errorf("mounts = %+v; want %+v", got, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		inputs []string
	}{
		{
			name:   "unexpanded_child_under_symlink_dir",
			inputs: []string{"frameworks/native/include/gui", "frameworks/native/include/gui/AdditionalOptions.h"},
		},
		{
			name:   "traversing_symlink_dir_with_abs_target",
			inputs: []string{"a/abs_sym", "a/abs_sym/foo.h"},
		},
		{
			name:   "traversing_symlink_dir_resolving_outside_workspace",
			inputs: []string{"a/escape_sym", "a/escape_sym/foo.h"},
		},
		{
			name:   "nonlocal_raw_input",
			inputs: []string{"../outside/foo.h"},
		},
		{
			name:   "symlink_cycle_on_disk",
			inputs: []string{"a/loop1", "a/loop1/foo.h"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jail, err := newWithInputs(tc.inputs)
			if err == nil {
				jail.Close()
				t.Fatalf("newWithInputs(%v) = nil err; want error", tc.inputs)
			}
		})
	}
}

func BenchmarkResolveInputs(b *testing.B) {
	dir := b.TempDir()
	var inputs []string
	// Simulate a typical Android C++ compile action with 500 header/tool inputs
	// across 25 include directories (depth 5-6), plus a symlinked include dir.
	for d := range 25 {
		for f := range 20 {
			rel := filepath.Join("packages", "modules", fmt.Sprintf("mod%02d", d), "include", "sub", fmt.Sprintf("hdr%02d.h", f))
			p := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("#pragma once\n"), 0644); err != nil {
				b.Fatal(err)
			}
			inputs = append(inputs, rel)
		}
	}
	symParent := filepath.Join(dir, "frameworks/native/include")
	if err := os.MkdirAll(symParent, 0755); err != nil {
		b.Fatal(err)
	}
	realGUI := filepath.Join(dir, "frameworks/native/libs/gui/include/gui")
	if err := os.MkdirAll(realGUI, 0755); err != nil {
		b.Fatal(err)
	}
	if err := os.Symlink("../libs/gui/include/gui", filepath.Join(symParent, "gui")); err != nil {
		b.Fatal(err)
	}
	for f := range 10 {
		if err := os.WriteFile(filepath.Join(realGUI, fmt.Sprintf("gui%02d.h", f)), []byte("#pragma once\n"), 0644); err != nil {
			b.Fatal(err)
		}
		inputs = append(inputs, filepath.ToSlash(filepath.Join("frameworks/native/libs/gui/include/gui", fmt.Sprintf("gui%02d.h", f))))
	}
	inputs = append(inputs, "frameworks/native/include/gui")

	hfs, err := hashfs.New(b.Context(), hashfs.Option{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := hfs.Close(b.Context()); err != nil {
			b.Fatal(err)
		}
	})
	fsys := hfs.FileSystem(b.Context(), dir)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if _, err := resolveInputs(fsys, inputs); err != nil {
			b.Fatal(err)
		}
	}
}
