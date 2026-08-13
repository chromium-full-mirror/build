// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package e2etests

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/ninjabuild"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/hashfs/osfs"
	"go.chromium.org/build/siso/reapi/reapitest"
	"go.chromium.org/build/siso/toolsupport/abfsutil"
)

func TestBuild_ABFS(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("abfs is only available on linux now")
	}
	if !runInSubProcess(t) {
		return
	}
	ctx := t.Context()
	dir := tempDir(t)

	runNinjaTest := func(t *testing.T, refake *reapitest.Fake, abfsClient *abfsutil.Client) (build.Stats, error) {
		t.Helper()
		var ds build.DataSource
		defer func() {
			err := ds.Close(ctx)
			if err != nil {
				t.Error(err)
			}
		}()
		ds.Client = reapitest.New(ctx, t, refake)
		ds.Cache = ds.Client.CacheStore()

		opt, graph, cleanup := setupBuild(ctx, t, dir, hashfs.Option{
			StateFile:  ".siso_fs_state",
			DataSource: ds,
			ABFS:       abfsClient,
		})
		defer cleanup()
		opt.REAPIClient = ds.Client
		return ninjabuild.Run(ctx, graph, opt, nil, ninjabuild.RunNinjaOpts{})
	}

	outData := []byte("foo.out content")
	var outDigest *rpb.Digest
	setupFiles(t, dir, t.Name(), nil)
	fakere := &reapitest.Fake{
		ExecuteFunc: func(fakere *reapitest.Fake, action *rpb.Action) (*rpb.ActionResult, error) {
			var err error
			outDigest, err = fakere.Put(ctx, outData)
			if err != nil {
				msg := fmt.Sprintf("failed to write gen/foo.out: %v", err)
				t.Log(msg)
				return &rpb.ActionResult{
					ExitCode:  1,
					StderrRaw: []byte(msg),
				}, nil
			}
			return &rpb.ActionResult{
				ExitCode: 0,
				OutputFiles: []*rpb.OutputFile{
					{
						Path:   "gen/foo.out",
						Digest: outDigest,
					},
				},
			}, nil
		},
	}

	ofs := osfs.New(ctx, "osfs", osfs.Option{})
	var (
		mu            sync.Mutex
		getRBEDigests = make(map[string]int)
		setRBEDigests = make(map[string]int)
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/mnt/get-rbe-digest", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "wrong method", http.StatusMethodNotAllowed)
			return
		}
		q := req.URL.Query()
		path := q.Get("path")
		mu.Lock()
		getRBEDigests[path]++
		mu.Unlock()
		fi, err := os.Lstat(filepath.Join(dir, path))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, fmt.Sprintf("lstat: %v", err), http.StatusInternalServerError)
			return
		}
		data, err := blob.FromLocalFile(ctx, digest.SHA256, ofs.FileSource(filepath.Join(dir, path), -1))
		if err != nil {
			http.Error(w, fmt.Sprintf("from local: %v", err), http.StatusInternalServerError)
			return
		}
		buf, err := json.Marshal(abfsutil.RBEPathStat{
			Path:   path,
			SHA256: data.Digest().Hash,
			Size:   data.Digest().SizeBytes,
			Mode:   uint32(fs.FileMode(syscall.S_IFREG) | fi.Mode()),
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("marshal error: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Add("Content-Type", "application/json")
		w.Write(buf)
	})
	mux.HandleFunc("/mnt/set-rbe-digests", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "wrong method", http.StatusMethodNotAllowed)
			return
		}
		reqBody, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("req body: %v", err), http.StatusInternalServerError)
			return
		}
		var reqMsg abfsutil.SetRBEDigestsReq
		err = json.Unmarshal(reqBody, &reqMsg)
		if err != nil {
			http.Error(w, fmt.Sprintf("req parse: %v", err), http.StatusInternalServerError)
			return
		}
		respMsg := make(map[string]string)
		for _, r := range reqMsg.Digests {
			mu.Lock()
			setRBEDigests[r.Path]++
			mu.Unlock()
			if r.SHA256 != outDigest.GetHash() || r.Size != outDigest.GetSizeBytes() {
				respMsg[r.Path] = fmt.Sprintf("unknown digest %s/%d", r.SHA256, r.Size)
				continue
			}
			err := os.WriteFile(filepath.Join(dir, r.Path), outData, fs.FileMode(r.Mode)&fs.ModePerm)
			if err != nil {
				respMsg[r.Path] = fmt.Sprintf("failed to write %q: %v", r.Path, err)
			}
		}
		respBody, err := json.Marshal(respMsg)
		if err != nil {
			http.Error(w, fmt.Sprintf("resp marshal: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Add("Content-Type", "application/json")
		w.Write(respBody)
	})
	tmpdir := t.TempDir()
	endpoint := filepath.Join(tmpdir, "abfs.sock")
	lis, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatalf("listen %s: %v", endpoint, err)
	}
	abfsServer := &httptest.Server{
		Listener: lis,
		Config:   &http.Server{Handler: mux},
	}
	abfsServer.Start()
	abfsServer.URL = "unix://" + endpoint
	t.Cleanup(abfsServer.Close)

	abfsClient, err := abfsutil.New(ctx, abfsServer.URL, dir)
	if err != nil {
		t.Fatalf("abfs: %v", err)
	}

	stats, err := runNinjaTest(t, fakere, abfsClient)
	if err != nil {
		t.Fatalf("ninja: %v", err)
	}
	if stats.Done != stats.Total || stats.Done != 1 {
		t.Errorf("done=%d total=%d; want done=total=1", stats.Done, stats.Total)
	}
	wantGetRBEDigests := map[string]int{
		"foo.in":               1,
		"out/siso/build.ninja": 1,
		"tools/cp.py":          1,
	}
	if diff := cmp.Diff(wantGetRBEDigests, getRBEDigests); diff != "" {
		t.Errorf("get_rbe_digests: diff -want +got:\n%s", diff)
	}

	wantSetRBEDigests := map[string]int{
		"out/siso/gen/foo.out": 1,
	}
	if diff := cmp.Diff(wantSetRBEDigests, setRBEDigests); diff != "" {
		t.Errorf("set_rbe_digests: diff -want +got:\n%s", diff)
	}
}
