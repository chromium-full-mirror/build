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
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	iradix "github.com/hashicorp/go-immutable-radix/v2"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
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
	srcDigest, err := cas.Put(digest.SHA256, srcContent)
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
	lowerDir, err := root.RegisterSandbox(sandboxID, trie, digest.SHA256, cas, fuseMountpoint, nil)
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

	warmupLowerDir, err := root.RegisterSandbox("bench-warmup", trie, digest.SHA256, cas, fuseMountpoint, nil)
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
			lowerDir, err := root.RegisterSandbox(sandboxID, trie, digest.SHA256, cas, fuseMountpoint, nil)
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
		Digest:   digest.Digest{Hash: "benchmark-shared-src-dir", SizeBytes: int64(len(files))},
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

	d := digest.Digest{Hash: fmt.Sprintf("%02x%062x", i%256, i+1), SizeBytes: size}
	path := cas.Path(digest.SHA256, d)

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
	lowerDir, err := root.RegisterSandbox(sandboxID, trie, digest.SHA256, cas, fuseMountpoint, nil)
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
	wantDigest := digest.SHA256.FromBytes(data)
	t.Logf("source file digest: %s/%d", wantDigest.Hash, wantDigest.SizeBytes)
}

// TestFuseOutputInodeLeak verifies that output directories from one sandbox
// do not show up in another sandbox that has the same input directory (same
// digest), and that RegisterSandbox doesn't create them in the FUSE layer.
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
	srcDigest, err := cas.Put(digest.SHA256, []byte("source"))
	if err != nil {
		t.Fatalf("cas.Put: %v", err)
	}

	// sharedDigest is the REAPI digest of the shared input directory, which
	// both tries reference.
	sharedDigest := digest.Digest{Hash: "shared-dir-digest-for-test", SizeBytes: 42}

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

	// Register sandbox A, which declares an output under "gen_a/".
	lowerA, err := root.RegisterSandbox("sandbox-a", trieA, digest.SHA256, cas, fuseMountpoint, nil)
	if err != nil {
		t.Fatalf("RegisterSandbox(a): %v", err)
	}
	t.Cleanup(func() { root.UnregisterSandbox("sandbox-a") })

	// Register sandbox B, which declares an output under "gen_b/".
	lowerB, err := root.RegisterSandbox("sandbox-b", trieB, digest.SHA256, cas, fuseMountpoint, nil)
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

// TestFuseDigestFunctionNamespacing verifies that two sandboxes whose input
// directories and files have the same digest hashes under different digest
// functions each serve their own function's blob, although file inodes are
// shared across sandboxes.
func TestFuseDigestFunctionNamespacing(t *testing.T) {
	if _, err := exec.LookPath("fusermount3"); err != nil {
		t.Skip("fusermount3 not found in PATH")
	}

	sha256treeFn, err := digest.ParseFunction("sha256tree")
	if err != nil {
		t.Fatal(err)
	}
	fns := []digest.Function{digest.SHA256, sha256treeFn}

	ctx := t.Context()
	cas, err := blobstore.NewWithOpts(ctx, t.TempDir(), blobstore.Options{DigestFunctions: fns})
	if err != nil {
		t.Fatalf("blobstore.NewWithOpts: %v", err)
	}

	// One file digest, two different contents planted directly at each
	// function's CAS path, so serving the wrong function's blob is
	// observable.
	contents := map[digest.Function]string{
		digest.SHA256: "AAAA",
		sha256treeFn:  "BBBB",
	}
	fileDigest := digest.Digest{Hash: fmt.Sprintf("%064x", 0xf00d), SizeBytes: 4}
	for fn, content := range contents {
		if err := os.WriteFile(cas.Path(fn, fileDigest), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// The input directory has the same digest hash under both functions.
	sharedDigest := digest.Digest{Hash: "shared-dir-digest-for-fn-test", SizeBytes: 42}
	txn := iradix.New[*model.KajiyaDirectory]().Txn()
	txn.Insert([]byte(""), &model.KajiyaDirectory{
		Dirs:     []string{"shared"},
		UnixMode: 0755,
	})
	txn.Insert([]byte("shared/"), &model.KajiyaDirectory{
		Digest: sharedDigest,
		Files: []model.KajiyaFile{
			{Name: "input", Digest: fileDigest, UnixMode: 0644},
		},
		UnixMode: 0755,
	})
	trie := txn.Commit()

	fuseMountpoint := t.TempDir()
	root, server, err := MountCASFS(fuseMountpoint)
	if err != nil {
		t.Fatalf("MountCASFS: %v", err)
	}
	t.Cleanup(func() {
		root.Close()
		server.Unmount()
	})

	for i, fn := range fns {
		sandboxID := fmt.Sprintf("fn-test-%d", i)
		lowerDir, err := root.RegisterSandbox(sandboxID, trie, fn, cas, fuseMountpoint, nil)
		if err != nil {
			t.Fatalf("RegisterSandbox(%v): %v", fn, err)
		}
		t.Cleanup(func() { root.UnregisterSandbox(sandboxID) })

		data, err := os.ReadFile(filepath.Join(lowerDir, "shared", "input"))
		if err != nil {
			t.Fatalf("ReadFile(%v): %v", fn, err)
		}
		if got, want := string(data), contents[fn]; got != want {
			t.Errorf("sandbox for %v read %q, want %q", fn, got, want)
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
			lowerDir, err := root.RegisterSandbox(sandboxID, trie, digest.SHA256, cas, fuseMountpoint, nil)
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

// TestFuseTracedSandbox verifies that file opens are recorded when a
// sandbox is registered with an AccessRecorder, and that non-traced
// sandboxes are unaffected.
func TestFuseTracedSandbox(t *testing.T) {
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

	// Register a traced sandbox.
	recorder := NewAccessRecorder()
	lowerDir, err := root.RegisterSandbox("traced", trie, digest.SHA256, cas, fuseMountpoint, recorder)
	if err != nil {
		t.Fatalf("RegisterSandbox(traced): %v", err)
	}
	t.Cleanup(func() { root.UnregisterSandbox("traced") })

	// Register a non-traced sandbox (nil recorder).
	lowerDirUntraced, err := root.RegisterSandbox("untraced", trie, digest.SHA256, cas, fuseMountpoint, nil)
	if err != nil {
		t.Fatalf("RegisterSandbox(untraced): %v", err)
	}
	t.Cleanup(func() { root.UnregisterSandbox("untraced") })

	// Read a file through the traced sandbox.
	data, err := os.ReadFile(filepath.Join(lowerDir, "src", "hello.cc"))
	if err != nil {
		t.Fatalf("ReadFile(traced): %v", err)
	}
	if got, want := string(data), "int main() { return 0; }\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}

	// Read through the untraced sandbox — should not affect the recorder.
	if _, err := os.ReadFile(filepath.Join(lowerDirUntraced, "src", "hello.cc")); err != nil {
		t.Fatalf("ReadFile(untraced): %v", err)
	}

	// Verify the recorder captured exactly the one file we opened.
	inputs := recorder.ObservedInputs()
	if got, want := len(inputs), 1; got != want {
		t.Fatalf("ObservedInputs() returned %d entries, want %d: %v", got, want, inputs)
	}
	if got, want := inputs[0], "src/hello.cc"; got != want {
		t.Errorf("ObservedInputs()[0] = %q, want %q", got, want)
	}
}

// TestFuseDirectoryInodePerPath checks that RegisterSandbox gives a directory
// that appears at two paths an inode per path and per sandbox, while the
// file in it keeps one inode everywhere.
func TestFuseDirectoryInodePerPath(t *testing.T) {
	skipUnlessFuseWorks(t)

	// Mount the FUSE filesystem.
	cas, err := blobstore.New(t.Context(), t.TempDir())
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

	// Register two sandboxes whose input roots hold the same directory at
	// a/ and b/. Both keys share one *KajiyaDirectory, so that neither its
	// digest nor its address tells the paths apart.
	srcDigest := putTestBlob(t, cas, []byte("source"))
	shared := &model.KajiyaDirectory{
		Digest:   digest.Digest{Hash: "shared-dir-digest-for-path-test", SizeBytes: 42},
		Files:    []model.KajiyaFile{{Name: "input.cc", Digest: srcDigest, UnixMode: 0644}},
		UnixMode: 0755,
	}
	txn := iradix.New[*model.KajiyaDirectory]().Txn()
	txn.Insert([]byte(""), &model.KajiyaDirectory{Dirs: []string{"a", "b"}, UnixMode: 0755})
	txn.Insert([]byte("a/"), shared)
	txn.Insert([]byte("b/"), shared)
	trie := txn.Commit()
	var lowerDirs []string
	for _, sandboxID := range []string{"path-test-1", "path-test-2"} {
		lowerDir, err := root.RegisterSandbox(sandboxID, trie, digest.SHA256, cas, fuseMountpoint, nil)
		if err != nil {
			t.Fatalf("RegisterSandbox(%s): %v", sandboxID, err)
		}
		t.Cleanup(func() { root.UnregisterSandbox(sandboxID) })
		lowerDirs = append(lowerDirs, lowerDir)
	}

	// Check that every directory path has an inode of its own, and that
	// every path of the file has the same one.
	inode := func(p string) uint64 {
		t.Helper()
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		return fi.Sys().(*syscall.Stat_t).Ino
	}
	dirPaths := map[uint64]string{}
	fileInodes := map[uint64]bool{}
	for _, lowerDir := range lowerDirs {
		for _, name := range []string{"a", "b"} {
			p := filepath.Join(lowerDir, name)
			ino := inode(p)
			if other, ok := dirPaths[ino]; ok {
				t.Errorf("%s has the inode of %s", p, other)
			}
			dirPaths[ino] = p
			fileInodes[inode(filepath.Join(p, "input.cc"))] = true
		}
	}
	if got, want := len(fileInodes), 1; got != want {
		t.Errorf("input.cc has %d distinct inodes, want %d", got, want)
	}
}

// TestFuseUnregisterSandboxUnlinksFiles checks that UnregisterSandbox removes
// the sandbox's links to the shared file inodes, which would otherwise keep
// its directories alive.
func TestFuseUnregisterSandboxUnlinksFiles(t *testing.T) {
	skipUnlessFuseWorks(t)

	// Mount the FUSE filesystem, then register and unregister a sandbox.
	cas, err := blobstore.New(t.Context(), t.TempDir())
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
	lowerDir, err := root.RegisterSandbox("unlink-test", buildTestTrie(t, cas), digest.SHA256, cas, fuseMountpoint, nil)
	if err != nil {
		t.Fatalf("RegisterSandbox: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(lowerDir, "src", "hello.cc")); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	root.UnregisterSandbox("unlink-test")

	// Check that the shared inode of the sandbox's only file has no parent
	// left.
	var files int
	root.fileInodes.Range(func(key, value any) bool {
		files++
		if name, parent := value.(*fs.Inode).Parent(); parent != nil {
			t.Errorf("file inode for %s is still linked as %q", key.(fileKey).casPath, name)
		}
		return true
	})
	if files == 0 {
		t.Errorf("fileInodes is empty, want the sandbox's file inode")
	}
}

// TestFuseSandboxSameDirectoryAtTwoPaths runs an action under the FuseFS
// strategy whose input root holds one directory at both a/x and b/x, and
// which copies a file out of each path into a declared output next to it.
func TestFuseSandboxSameDirectoryAtTwoPaths(t *testing.T) {
	skipUnlessNsjailWorks(t)
	skipUnlessFuseWorks(t)

	// Store an input root that holds the same directory x/ in a/ and in b/,
	// which differ otherwise.
	cas, err := blobstore.New(t.Context(), filepath.Join(t.TempDir(), "cas"))
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}
	fileDigest := putTestBlob(t, cas, []byte("hello\n"))
	x := putTestMessage(t, cas, &repb.Directory{
		Files: []*repb.FileNode{{Name: "f", Digest: fileDigest.Proto()}},
	})
	a := putTestMessage(t, cas, &repb.Directory{
		Directories: []*repb.DirectoryNode{{Name: "x", Digest: x}},
	})
	b := putTestMessage(t, cas, &repb.Directory{
		Files:       []*repb.FileNode{{Name: "g", Digest: putTestBlob(t, cas, []byte("only in b\n")).Proto()}},
		Directories: []*repb.DirectoryNode{{Name: "x", Digest: x}},
	})
	inputRoot := putTestMessage(t, cas, &repb.Directory{
		Directories: []*repb.DirectoryNode{{Name: "a", Digest: a}, {Name: "b", Digest: b}},
	})

	// Run the action, and check that both copies succeeded.
	result := runFuseTestAction(t, cas, inputRoot,
		[]string{"/bin/sh", "-c", "cp a/x/f a/x/out && cp b/x/f b/x/out"},
		[]string{"a/x/out", "b/x/out"})
	var outputs []string
	for _, f := range result.OutputFiles {
		outputs = append(outputs, f.Path+" "+f.Digest.Hash)
	}
	if got, want := outputs, []string{"a/x/out " + fileDigest.Hash, "b/x/out " + fileDigest.Hash}; !slices.Equal(got, want) {
		t.Errorf("output files = %v, want %v", got, want)
	}
}

// TestFuseIdenticalFilesKeepTheirAttributes checks that files with the same
// content but different modes or mtimes each report their own, in one
// sandbox and across sandboxes.
func TestFuseIdenticalFilesKeepTheirAttributes(t *testing.T) {
	skipUnlessFuseWorks(t)

	// Mount the FUSE filesystem.
	cas, err := blobstore.New(t.Context(), t.TempDir())
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

	// Register two sandboxes, each with three files of the same content.
	d := putTestBlob(t, cas, []byte("#!/bin/sh\n"))
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 6, time.UTC)
	files := []model.KajiyaFile{
		{Name: "data", Digest: d, UnixMode: 0644},
		{Name: "stamped", Digest: d, UnixMode: 0644, Mtime: mtime},
		{Name: "tool", Digest: d, UnixMode: 0755},
	}
	txn := iradix.New[*model.KajiyaDirectory]().Txn()
	txn.Insert([]byte(""), &model.KajiyaDirectory{Files: files, UnixMode: 0755})
	trie := txn.Commit()
	var lowerDirs []string
	for _, sandboxID := range []string{"attr-test-1", "attr-test-2"} {
		lowerDir, err := root.RegisterSandbox(sandboxID, trie, digest.SHA256, cas, fuseMountpoint, nil)
		if err != nil {
			t.Fatalf("RegisterSandbox(%s): %v", sandboxID, err)
		}
		t.Cleanup(func() { root.UnregisterSandbox(sandboxID) })
		lowerDirs = append(lowerDirs, lowerDir)
	}

	// Check every file's mode and mtime in both sandboxes.
	for _, lowerDir := range lowerDirs {
		for _, f := range files {
			fi, err := os.Stat(filepath.Join(lowerDir, f.Name))
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if got, want := fi.Mode().Perm(), f.UnixMode; got != want {
				t.Errorf("%s: mode = %v, want %v", f.Name, got, want)
			}
			if got, want := fi.ModTime(), f.Mtime; !got.Equal(want) {
				t.Errorf("%s: mtime = %v, want %v", f.Name, got, want)
			}
		}
	}
}

// TestFuseSandboxWriteToIdenticalFile runs an action under the FuseFS
// strategy that appends to one of two input files with the same content,
// and checks that the other one keeps its content.
func TestFuseSandboxWriteToIdenticalFile(t *testing.T) {
	skipUnlessNsjailWorks(t)
	skipUnlessFuseWorks(t)

	// Store an input root that holds the same file at a/f and b/f, in
	// directories that differ otherwise.
	cas, err := blobstore.New(t.Context(), filepath.Join(t.TempDir(), "cas"))
	if err != nil {
		t.Fatalf("blobstore.New: %v", err)
	}
	fileDigest := putTestBlob(t, cas, []byte("hello\n")).Proto()
	a := putTestMessage(t, cas, &repb.Directory{
		Files: []*repb.FileNode{{Name: "f", Digest: fileDigest}},
	})
	b := putTestMessage(t, cas, &repb.Directory{
		Files: []*repb.FileNode{
			{Name: "f", Digest: fileDigest},
			{Name: "g", Digest: putTestBlob(t, cas, []byte("only in b\n")).Proto()},
		},
	})
	inputRoot := putTestMessage(t, cas, &repb.Directory{
		Directories: []*repb.DirectoryNode{{Name: "a", Digest: a}, {Name: "b", Digest: b}},
	})

	// Run the action, and check what it read from b/f.
	result := runFuseTestAction(t, cas, inputRoot, []string{"/bin/sh", "-c", "echo x >> a/f && cat b/f"}, nil)
	if got, want := readTestBlob(t, cas, result.StdoutDigest), "hello\n"; got != want {
		t.Errorf("b/f = %q, want %q", got, want)
	}
}

// runFuseTestAction runs args under the FuseFS strategy on the host
// filesystem, with the input root inputRoot and the declared outputs
// outputs. It fails the test unless the action exits with code 0, and
// returns the action's result.
func runFuseTestAction(t *testing.T, cas *blobstore.ContentAddressableStorage, inputRoot *repb.Digest, args, outputs []string) *repb.ActionResult {
	t.Helper()

	// Store the action.
	actionDigest, err := digest.SHA256.FromProto(putTestMessage(t, cas, &repb.Action{
		CommandDigest:   putTestMessage(t, cas, &repb.Command{Arguments: args, OutputPaths: outputs}),
		InputRootDigest: inputRoot,
	}))
	if err != nil {
		t.Fatalf("FromProto: %v", err)
	}

	// Run it in a new executor.
	executor, err := New(filepath.Join(t.TempDir(), "exec"), cas, FuseFS, true, false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := executor.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	action, err := model.LoadAction(digest.SHA256, actionDigest, cas)
	if err != nil {
		t.Fatalf("LoadAction: %v", err)
	}
	result, err := executor.Execute(action)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Check that it succeeded.
	if got, want := result.ExitCode, int32(0); got != want {
		t.Fatalf("exit code = %d, want %d; stderr: %s", got, want, readTestBlob(t, cas, result.StderrDigest))
	}
	return result
}
