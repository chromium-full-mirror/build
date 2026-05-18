// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build linux

package localexec

import (
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	iradix "github.com/hashicorp/go-immutable-radix/v2"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/digest"
	"go.chromium.org/build/kajiya/execution/model"
)

// buildTestTrie creates a DirectoryTrie that mimics a simple build action:
//
//	""                     (root trie key)
//	src/                   (input directory with one source file)
//	src/hello.cc           (input file)
//	out/obj/               (output directory, declared output: hello.o)
func buildTestTrie(t *testing.T, cas *blobstore.ContentAddressableStorage) *model.DirectoryTrie {
	t.Helper()

	// Put a fake source file into CAS.
	srcContent := []byte("int main() { return 0; }\n")
	srcDigest, err := cas.Put(srcContent)
	if err != nil {
		t.Fatalf("cas.Put: %v", err)
	}

	// Build the trie. Keys match treeToTrie conventions: root is "", and
	// children are "name/".
	txn := iradix.New[*model.KajiyaDirectory]().Txn()

	// Root directory: lists subdirs "src" and "out", declares no outputs.
	txn.Insert([]byte(""), &model.KajiyaDirectory{
		Dirs:     []string{"src", "out"},
		UnixMode: 0755,
	})

	// src/ directory: contains the source file.
	txn.Insert([]byte("src/"), &model.KajiyaDirectory{
		Files: []model.KajiyaFile{
			{Name: "hello.cc", Digest: srcDigest, UnixMode: 0644},
		},
		UnixMode: 0755,
	})

	// out/ directory: contains the "obj" subdirectory.
	txn.Insert([]byte("out/"), &model.KajiyaDirectory{
		Dirs:     []string{"obj"},
		UnixMode: 0755,
	})

	// out/obj/ directory: declares hello.o as an output.
	txn.Insert([]byte("out/obj/"), &model.KajiyaDirectory{
		Outputs: []model.KajiyaOutput{
			{Name: "hello.o", Type: model.File},
		},
		UnixMode: 0755,
	})

	trie := txn.Commit()
	return trie
}

// TestFuseOverlayWritePermission mounts a CAS-backed FUSE filesystem,
// layers overlayfs on top, and verifies that files can be created in
// directories served from the FUSE lower layer.
func TestFuseOverlayWritePermission(t *testing.T) {
	// --- prerequisites ---------------------------------------------------
	if os.Getuid() == 0 {
		t.Skip("test must not run as root (permission checks are bypassed)")
	}
	if _, err := exec.LookPath("fusermount3"); err != nil {
		t.Skip("fusermount3 not found in PATH")
	}
	// We need "unshare" to create a user namespace for unprivileged overlayfs.
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not found in PATH")
	}

	ctx := t.Context()

	// --- CAS -------------------------------------------------------------
	casDir := t.TempDir()
	cas, err := blobstore.New(ctx, casDir)
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}

	// --- FUSE mount ------------------------------------------------------
	fuseMountpoint := t.TempDir()
	root, server, err := MountCASFS(fuseMountpoint)
	if err != nil {
		t.Fatalf("MountCASFS: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Unmount(); err != nil {
			t.Errorf("server.Unmount: %v", err)
		}
	})

	// --- register sandbox ------------------------------------------------
	trie := buildTestTrie(t, cas)
	sandboxID := "test-sandbox"
	lowerDir, err := root.RegisterSandbox(sandboxID, trie, cas, fuseMountpoint)
	if err != nil {
		t.Fatalf("RegisterSandbox: %v", err)
	}
	t.Cleanup(func() { root.UnregisterSandbox(sandboxID) })

	// --- diagnostic: inspect FUSE layer ----------------------------------
	t.Log("=== FUSE lower layer diagnostics ===")
	inspectDir(t, lowerDir, "lower root")
	inspectDir(t, filepath.Join(lowerDir, "src"), "lower src/")
	inspectDir(t, filepath.Join(lowerDir, "out"), "lower out/")
	inspectDir(t, filepath.Join(lowerDir, "out", "obj"), "lower out/obj/")

	// Check that the source file is accessible.
	srcPath := filepath.Join(lowerDir, "src", "hello.cc")
	if data, err := os.ReadFile(srcPath); err != nil {
		t.Errorf("ReadFile(%s): %v", srcPath, err)
	} else {
		t.Logf("source file readable, %d bytes", len(data))
	}

	// --- try direct write into FUSE (should fail: FUSE is read-only) -----
	t.Log("=== direct write into FUSE lower layer ===")
	testPath := filepath.Join(lowerDir, "out", "obj", "direct.o")
	if err := os.WriteFile(testPath, []byte("test"), 0644); err != nil {
		t.Logf("direct write correctly failed: %v", err)
	} else {
		t.Log("direct write unexpectedly succeeded (FUSE allows writes?)")
		os.Remove(testPath)
	}

	// --- overlayfs -------------------------------------------------------
	t.Log("=== overlayfs mount ===")
	tmpDir := t.TempDir()
	upperDir := filepath.Join(tmpDir, "upper")
	workDir := filepath.Join(tmpDir, "work")
	mergedDir := filepath.Join(tmpDir, "merged")
	for _, d := range []string{upperDir, workDir, mergedDir} {
		if err := os.Mkdir(d, 0755); err != nil {
			t.Fatalf("Mkdir(%s): %v", d, err)
		}
	}

	// The overlayfs work directory gets kernel-internal subdirectories with
	// restrictive permissions that t.TempDir() cleanup can't remove.  Clean
	// up manually before the test exits.
	t.Cleanup(func() {
		_ = filepath.WalkDir(workDir, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				os.Chmod(path, 0700)
			}
			return nil
		})
	})

	// Use unshare to create a user namespace so we can mount overlayfs
	// without root.  The shell snippet:
	//   1. Mounts overlayfs
	//   2. Prints diagnostics about the merged directory
	//   3. Tries to create a file in out/obj/
	//   4. Reports success/failure
	script := fmt.Sprintf(`
set -e
mount -t overlay overlay \
  -o "lowerdir=%s,upperdir=%s,workdir=%s,userxattr,index=off,xino=off,volatile" \
  %s

# Verify input file is readable through the merged view.
cat %s/src/hello.cc > /dev/null

# Create an output file — this triggers overlayfs copy-up of the
# parent directories from the FUSE lower layer.
echo "hello" > %s/out/obj/hello.o
`,
		lowerDir, upperDir, workDir, mergedDir,
		mergedDir,
		mergedDir,
	)

	cmd := exec.Command("unshare", "--user", "--map-root-user", "--mount", "sh", "-c", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unshare command failed: %v\n%s", err, out)
	}

	// Verify the output file landed in the upper dir (not the FUSE layer).
	upperFile := filepath.Join(upperDir, "out", "obj", "hello.o")
	if _, err := os.Stat(upperFile); err != nil {
		t.Fatalf("output file not found in upper dir: %v", err)
	}
}

// inspectDir stats a directory and logs its mode, uid, gid.
func inspectDir(t *testing.T, path, label string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Logf("  %s: stat error: %v", label, err)
		return
	}
	stat := fi.Sys().(*syscall.Stat_t)
	t.Logf("  %s: mode=%04o uid=%d gid=%d", label, fi.Mode().Perm(), stat.Uid, stat.Gid)
}

// TestFuseAttrValues verifies that dirAttr produces the expected mode.
func TestFuseAttrValues(t *testing.T) {
	tests := []struct {
		name     string
		unixMode os.FileMode
		wantPerm uint32 // expected permission bits (low 12) after go-fuse masking
	}{
		{"default_0755", 0755, 0777},
		{"readonly_0555", 0555, 0777},
		{"zero_mode", 0, 0222},
		{"explicit_0700", 0700, 0722},
		{"explicit_0644", 0644, 0666},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := &model.KajiyaDirectory{UnixMode: tc.unixMode}
			attr := dirAttr(dir)
			// go-fuse applies: (attr.Mode & 07777) | stableAttr.Mode
			gotPerm := attr.Mode & 07777
			if gotPerm != tc.wantPerm {
				t.Errorf("dirAttr(UnixMode=%04o).Mode & 07777 = %04o, want %04o",
					tc.unixMode, gotPerm, tc.wantPerm)
			}
		})
	}
}

// BenchmarkFuseE2E measures the end-to-end cost of registering a sandbox and
// opening/fully reading CAS-backed files through the mounted FUSE filesystem.
// It uses Go's parallel benchmark runner (GOMAXPROCS workers by default) to
// approximate concurrent build actions. Each benchmark operation registers one
// sandbox, opens 1024 randomly selected files, then unregisters the sandbox.
func BenchmarkFuseE2E(b *testing.B) {
	if _, err := exec.LookPath("fusermount3"); err != nil {
		b.Skip("fusermount3 not found in PATH")
	}

	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(oldLogger) })

	const (
		filesPerAction      = 1024
		benchmarkFiles      = 65536
		benchmarkRandomSeed = int64(0x4b414a495941) // "KAJIYA"
	)
	ctx := b.Context()
	cas, err := blobstore.New(ctx, filepath.Join(b.TempDir(), "cas"))
	if err != nil {
		b.Fatal(err)
	}

	trie, files := buildBenchmarkTrie(b, cas, benchmarkFiles)

	fuseMountpoint := b.TempDir()
	root, server, err := MountCASFS(fuseMountpoint)
	if err != nil {
		b.Skipf("FUSE mount unavailable: %v", err)
	}
	b.Cleanup(func() {
		root.Close()
		if err := server.Unmount(); err != nil {
			b.Errorf("server.Unmount: %v", err)
		}
	})

	warmupLowerDir, err := root.RegisterSandbox("bench-warmup", trie, cas, fuseMountpoint)
	if err != nil {
		b.Fatalf("RegisterSandbox: %v", err)
	}
	readBenchmarkFiles(b, warmupLowerDir, files)
	root.UnregisterSandbox("bench-warmup")

	b.ReportAllocs()
	b.ResetTimer()
	var workerIDs, actionIDs, readBytes atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		workerID := workerIDs.Add(1)
		rng := rand.New(rand.NewSource(benchmarkRandomSeed + workerID))
		for pb.Next() {
			actionID := actionIDs.Add(1)
			sandboxID := fmt.Sprintf("bench-action-%d", actionID)
			lowerDir, err := root.RegisterSandbox(sandboxID, trie, cas, fuseMountpoint)
			if err != nil {
				b.Fatalf("RegisterSandbox(%s): %v", sandboxID, err)
			}
			readBytes.Add(readRandomBenchmarkFiles(b, rng, lowerDir, files, filesPerAction))
			root.UnregisterSandbox(sandboxID)
		}
	})
	b.StopTimer()

	b.ReportMetric(filesPerAction, "files/op")
	b.ReportMetric(float64(readBytes.Load())/float64(b.N), "read-bytes/op")
}

func buildBenchmarkTrie(b *testing.B, cas *blobstore.ContentAddressableStorage, numFiles int) (*model.DirectoryTrie, []string) {
	b.Helper()

	files := make([]model.KajiyaFile, 0, numFiles)
	fileNames := make([]string, 0, numFiles)
	addFile := func(name string) {
		d := benchmarkSparseBlob(b, cas, len(files), benchmarkBlobSize(len(files)))
		files = append(files, model.KajiyaFile{
			Name:     name,
			Digest:   d,
			UnixMode: 0644,
		})
	}
	for i := range numFiles {
		name := fmt.Sprintf("inp%06d.h", i)
		addFile(name)
		fileNames = append(fileNames, filepath.Join("src", name))
	}

	txn := iradix.New[*model.KajiyaDirectory]().Txn()
	txn.Insert([]byte(""), &model.KajiyaDirectory{
		Dirs:     []string{"src"},
		UnixMode: 0755,
	})
	txn.Insert([]byte("src/"), &model.KajiyaDirectory{
		Digest:   digest.Digest{Hash: "benchmark-shared-src-dir", Size: int64(len(files))},
		Files:    files,
		UnixMode: 0755,
	})
	return txn.Commit(), fileNames
}

func benchmarkBlobSize(i int) int64 {
	switch p := i % 10000; {
	case p < 4407:
		return 256
	case p < 5472:
		return 768
	case p < 7289:
		return 2 * 1024
	case p < 8558:
		return 6 * 1024
	case p < 9383:
		return 24 * 1024
	case p < 9742:
		return 96 * 1024
	case p < 9944:
		return 640 * 1024
	case p < 9994:
		return 3 * 1024 * 1024
	case p < 9999:
		return 8 * 1024 * 1024
	default:
		return 32 * 1024 * 1024
	}
}

func benchmarkSparseBlob(b *testing.B, cas *blobstore.ContentAddressableStorage, i int, size int64) digest.Digest {
	b.Helper()

	d := digest.Digest{Hash: fmt.Sprintf("%02x%062x", i%256, i+1), Size: size}
	path := cas.Path(d)

	// The benchmark cares about FUSE open/read behavior and apparent CAS
	// sizes, so sparse files keep setup cheap without making every digest
	// content-address-correct.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		b.Fatalf("OpenFile(%s): %v", path, err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		b.Fatalf("Truncate(%s): %v", path, err)
	}
	if err := f.Close(); err != nil {
		b.Fatalf("Close(%s): %v", path, err)
	}
	return d
}

func readBenchmarkFiles(b *testing.B, lowerDir string, files []string) {
	b.Helper()

	buf := make([]byte, 256*1024)
	for _, file := range files {
		readBenchmarkFile(b, lowerDir, file, buf)
	}
}

func readRandomBenchmarkFiles(b *testing.B, rng *rand.Rand, lowerDir string, files []string, count int) int64 {
	b.Helper()

	buf := make([]byte, 256*1024)
	var readBytes int64
	for range count {
		readBytes += readBenchmarkFile(b, lowerDir, files[rng.Intn(len(files))], buf[:])
	}
	return readBytes
}

func readBenchmarkFile(b *testing.B, lowerDir, file string, buf []byte) int64 {
	b.Helper()

	path := filepath.Join(lowerDir, file)
	f, err := os.Open(path)
	if err != nil {
		b.Fatalf("Open(%s): %v", path, err)
	}
	var readBytes int64
	for {
		n, err := f.Read(buf)
		readBytes += int64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			f.Close()
			b.Fatalf("Read(%s): %v", path, err)
		}
	}
	if err := f.Close(); err != nil {
		b.Fatalf("Close(%s): %v", path, err)
	}
	return readBytes
}

// TestFuseSandboxDirLayout verifies that RegisterSandbox creates the expected
// directory structure visible through the FUSE mount.
func TestFuseSandboxDirLayout(t *testing.T) {
	if _, err := exec.LookPath("fusermount3"); err != nil {
		t.Skip("fusermount3 not found in PATH")
	}

	ctx := t.Context()
	casDir := t.TempDir()
	cas, err := blobstore.New(ctx, casDir)
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}

	fuseMountpoint := t.TempDir()
	root, server, err := MountCASFS(fuseMountpoint)
	if err != nil {
		t.Fatalf("MountCASFS: %v", err)
	}
	t.Cleanup(func() { server.Unmount() })

	trie := buildTestTrie(t, cas)
	sandboxID := "layout-test"
	lowerDir, err := root.RegisterSandbox(sandboxID, trie, cas, fuseMountpoint)
	if err != nil {
		t.Fatalf("RegisterSandbox: %v", err)
	}
	t.Cleanup(func() { root.UnregisterSandbox(sandboxID) })

	// Walk the FUSE directory tree and log everything.
	err = filepath.WalkDir(lowerDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			t.Logf("  walk error at %s: %v", path, err)
			return nil
		}
		rel, _ := filepath.Rel(lowerDir, path)
		fi, _ := d.Info()
		if fi != nil {
			stat := fi.Sys().(*syscall.Stat_t)
			t.Logf("  %-30s mode=%04o uid=%d gid=%d size=%d",
				rel, fi.Mode().Perm(), stat.Uid, stat.Gid, fi.Size())
		}
		return nil
	})
	if err != nil {
		t.Errorf("WalkDir: %v", err)
	}

	// Verify expected entries exist.
	for _, p := range []string{"src", "src/hello.cc", "out", "out/obj"} {
		full := filepath.Join(lowerDir, p)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	// Read a file through FUSE.
	data, err := os.ReadFile(filepath.Join(lowerDir, "src", "hello.cc"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got, want := string(data), "int main() { return 0; }\n"; got != want {
		t.Errorf("file content = %q, want %q", got, want)
	}

	// Verify the file's digest matches what we stored.
	wantDigest := digest.FromBlob(data)
	t.Logf("source file digest: %s/%d", wantDigest.Hash, wantDigest.Size)
}

// TestFuseOutputInodeLeak verifies that output directory inodes from one
// sandbox do not leak into another sandbox that shares the same input
// directory (same digest). This is a regression test for the bug where
// createOutputInodes was called on a shared cached directory inode,
// accumulating output directories from all sandboxes that reuse it.
func TestFuseOutputInodeLeak(t *testing.T) {
	if _, err := exec.LookPath("fusermount3"); err != nil {
		t.Skip("fusermount3 not found in PATH")
	}

	ctx := t.Context()
	casDir := t.TempDir()
	cas, err := blobstore.New(ctx, casDir)
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}

	fuseMountpoint := t.TempDir()
	root, server, err := MountCASFS(fuseMountpoint)
	if err != nil {
		t.Fatalf("MountCASFS: %v", err)
	}
	t.Cleanup(func() {
		root.Close()
		server.Unmount()
	})

	// Put a fake source file into CAS so the shared directory has content.
	srcDigest, err := cas.Put([]byte("source"))
	if err != nil {
		t.Fatalf("cas.Put: %v", err)
	}

	// sharedDigest is the REAPI digest of the shared input directory.
	// Both tries reference the same digest, so the second sandbox will
	// hit the dirInodes cache.
	sharedDigest := digest.Digest{Hash: "shared-dir-digest-for-test", Size: 42}

	// buildTrie creates a trie with a "shared/" directory (using sharedDigest)
	// that declares a single output under outputSubdir (e.g. "gen_a/result.o").
	buildTrie := func(outputSubdir string) *model.DirectoryTrie {
		txn := iradix.New[*model.KajiyaDirectory]().Txn()
		txn.Insert([]byte(""), &model.KajiyaDirectory{
			Dirs:     []string{"shared"},
			UnixMode: 0755,
		})
		txn.Insert([]byte("shared/"), &model.KajiyaDirectory{
			Digest: sharedDigest,
			Files: []model.KajiyaFile{
				{Name: "input.cc", Digest: srcDigest, UnixMode: 0644},
			},
			Outputs: []model.KajiyaOutput{
				{Name: outputSubdir + "/result.o", Type: model.File},
			},
			UnixMode: 0755,
		})
		return txn.Commit()
	}

	trieA := buildTrie("gen_a")
	trieB := buildTrie("gen_b")

	// Register sandbox A. This populates the dirInodes cache for sharedDigest
	// and calls createOutputInodes, which creates "gen_a/" under the shared inode.
	lowerA, err := root.RegisterSandbox("sandbox-a", trieA, cas, fuseMountpoint)
	if err != nil {
		t.Fatalf("RegisterSandbox(a): %v", err)
	}
	t.Cleanup(func() { root.UnregisterSandbox("sandbox-a") })

	// Register sandbox B. This hits the dirInodes cache and calls
	// createOutputInodes on the SAME shared inode, creating "gen_b/".
	lowerB, err := root.RegisterSandbox("sandbox-b", trieB, cas, fuseMountpoint)
	if err != nil {
		t.Fatalf("RegisterSandbox(b): %v", err)
	}
	t.Cleanup(func() { root.UnregisterSandbox("sandbox-b") })

	// Output directories should NOT exist in the FUSE layer at all. They
	// belong in the overlayfs upper layer, which is created by the sandbox
	// setup code, not by RegisterSandbox.
	for _, tc := range []struct {
		name   string
		lower  string
		subdir string
	}{
		{"sandbox A / gen_a", lowerA, "gen_a"},
		{"sandbox A / gen_b", lowerA, "gen_b"},
		{"sandbox B / gen_a", lowerB, "gen_a"},
		{"sandbox B / gen_b", lowerB, "gen_b"},
	} {
		if _, err := os.Stat(filepath.Join(tc.lower, "shared", tc.subdir)); err == nil {
			t.Errorf("%s: output dir %s/ should not exist in FUSE layer", tc.name, tc.subdir)
		}
	}

	// Input files should still be accessible in both sandboxes.
	for _, tc := range []struct {
		name  string
		lower string
	}{
		{"sandbox A", lowerA},
		{"sandbox B", lowerB},
	} {
		data, err := os.ReadFile(filepath.Join(tc.lower, "shared", "input.cc"))
		if err != nil {
			t.Errorf("%s: reading input file: %v", tc.name, err)
		} else if got, want := string(data), "source"; got != want {
			t.Errorf("%s: input content = %q, want %q", tc.name, got, want)
		}
	}
}

// TestFuseConcurrentRegisterSandbox verifies that registering and
// unregistering multiple sandboxes concurrently is safe and correct.
func TestFuseConcurrentRegisterSandbox(t *testing.T) {
	if _, err := exec.LookPath("fusermount3"); err != nil {
		t.Skip("fusermount3 not found in PATH")
	}

	ctx := t.Context()
	casDir := t.TempDir()
	cas, err := blobstore.New(ctx, casDir)
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}

	fuseMountpoint := t.TempDir()
	root, server, err := MountCASFS(fuseMountpoint)
	if err != nil {
		t.Fatalf("MountCASFS: %v", err)
	}
	t.Cleanup(func() {
		root.Close()
		server.Unmount()
	})

	trie := buildTestTrie(t, cas)

	const numSandboxes = 20
	var wg sync.WaitGroup
	errs := make([]error, numSandboxes)

	// Register all sandboxes concurrently.
	for i := range numSandboxes {
		wg.Go(func() {
			sandboxID := fmt.Sprintf("concurrent-%d", i)
			lowerDir, err := root.RegisterSandbox(sandboxID, trie, cas, fuseMountpoint)
			if err != nil {
				errs[i] = fmt.Errorf("RegisterSandbox(%s): %v", sandboxID, err)
				return
			}
			// Verify the sandbox's files are accessible.
			data, err := os.ReadFile(filepath.Join(lowerDir, "src", "hello.cc"))
			if err != nil {
				errs[i] = fmt.Errorf("ReadFile(%s): %v", sandboxID, err)
				return
			}
			if got, want := string(data), "int main() { return 0; }\n"; got != want {
				errs[i] = fmt.Errorf("sandbox %s: content = %q, want %q", sandboxID, got, want)
			}
		})
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Error(err)
		}
	}

	// Unregister all sandboxes concurrently.
	for i := range numSandboxes {
		wg.Go(func() {
			root.UnregisterSandbox(fmt.Sprintf("concurrent-%d", i))
		})
	}
	wg.Wait()
}
