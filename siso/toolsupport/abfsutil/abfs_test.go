// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package abfsutil_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/merkletree"
	"go.chromium.org/build/siso/toolsupport/abfsutil"
)

func newTestServer(t *testing.T, h http.Handler) string {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "abfs.sock")
	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sockPath, err)
	}
	srv := &httptest.Server{
		Listener: lis,
		Config:   &http.Server{Handler: h},
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return "unix://" + sockPath
}

func TestRegisterFiles_Unsupported(t *testing.T) {
	entries := []*abfsutil.Registration{
		{
			Entry: merkletree.Entry{
				Name: path.Path("out/Default/gen/foo.h"),
				Data: blob.NewData(nil, digest.Digest{Hash: "abc", SizeBytes: 10}),
			},
		},
	}

	tests := []struct {
		name   string
		client *abfsutil.Client
	}{
		{
			name:   "nil_client",
			client: nil,
		},
		{
			name:   "empty_client_struct",
			client: &abfsutil.Client{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			err := tc.client.RegisterFiles(ctx, "/src", entries)
			if !errors.Is(err, errors.ErrUnsupported) {
				t.Errorf("RegisterFiles() = %v, want %v", err, errors.ErrUnsupported)
			}
		})
	}
}

func TestRegisterFiles_EmptyBody(t *testing.T) {
	ctx := t.Context()
	rootDir := "/src"
	buildDir := filepath.Join(rootDir, "out/Default")

	var gotReq abfsutil.SetRBEDigestsReq
	var gotMethod, gotPath, gotContentType string

	endpoint := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &gotReq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Return 200 OK with empty body.
		w.WriteHeader(http.StatusOK)
	}))

	client, err := abfsutil.New(ctx, endpoint, rootDir)
	if err != nil {
		t.Fatalf("abfsutil.New() = %v", err)
	}
	defer client.Close()

	d1 := digest.Digest{Hash: "hash1", SizeBytes: 123}
	d2 := digest.Digest{Hash: "hash2", SizeBytes: 456}

	entries := []*abfsutil.Registration{
		{
			Entry: merkletree.Entry{
				Name:         path.Path("gen/foo.h"),
				Data:         blob.NewData(nil, d1),
				IsExecutable: false,
			},
		},
		{
			Entry: merkletree.Entry{
				Name:         path.Path("bin/bar"),
				Data:         blob.NewData(nil, d2),
				IsExecutable: true,
			},
		},
	}

	err = client.RegisterFiles(ctx, buildDir, entries)
	if err != nil {
		t.Fatalf("RegisterFiles() = %v, want nil", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want %q", gotMethod, http.MethodPost)
	}
	if gotPath != "/mnt/set-rbe-digests" {
		t.Errorf("request path = %q, want %q", gotPath, "/mnt/set-rbe-digests")
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", gotContentType, "application/json")
	}

	wantReq := abfsutil.SetRBEDigestsReq{
		Digests: []abfsutil.RBEPathStat{
			{
				Path:   "out/Default/gen/foo.h",
				SHA256: "hash1",
				Size:   123,
				Mode:   uint32(syscall.S_IFREG | 0o644),
			},
			{
				Path:   "out/Default/bin/bar",
				SHA256: "hash2",
				Size:   456,
				Mode:   uint32(syscall.S_IFREG | 0o755),
			},
		},
	}

	if diff := cmp.Diff(wantReq, gotReq); diff != "" {
		t.Errorf("SetRBEDigestsReq diff (-want +got):\n%s", diff)
	}

	for i, ent := range entries {
		if ent.Err != nil {
			t.Errorf("entries[%d].Err = %v, want nil", i, ent.Err)
		}
	}
}

func TestRegisterFiles_PartialErrors(t *testing.T) {
	ctx := t.Context()
	rootDir := "/src"
	buildDir := filepath.Join(rootDir, "out/Default")

	endpoint := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]string{
			"out/Default/gen/failed.h": "permission denied",
		}
		respBody, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(respBody)
	}))

	client, err := abfsutil.New(ctx, endpoint, rootDir)
	if err != nil {
		t.Fatalf("abfsutil.New() = %v", err)
	}
	defer client.Close()

	entSuccess := &abfsutil.Registration{
		Entry: merkletree.Entry{
			Name: path.Path("gen/success.h"),
			Data: blob.NewData(nil, digest.Digest{Hash: "hash-success", SizeBytes: 10}),
		},
	}
	entFailed := &abfsutil.Registration{
		Entry: merkletree.Entry{
			Name: path.Path("gen/failed.h"),
			Data: blob.NewData(nil, digest.Digest{Hash: "hash-failed", SizeBytes: 20}),
		},
	}

	entries := []*abfsutil.Registration{entSuccess, entFailed}
	err = client.RegisterFiles(ctx, buildDir, entries)
	if err != nil {
		t.Fatalf("RegisterFiles() = %v, want nil", err)
	}

	if entSuccess.Err != nil {
		t.Errorf("entSuccess.Err = %v, want nil", entSuccess.Err)
	}
	if entFailed.Err == nil || entFailed.Err.Error() != "permission denied" {
		t.Errorf("entFailed.Err = %v, want %q", entFailed.Err, "permission denied")
	}
}

func TestRegisterFiles_EmptyJSONResponse(t *testing.T) {
	ctx := t.Context()
	rootDir := "/src"
	buildDir := filepath.Join(rootDir, "out/Default")

	endpoint := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
	}))

	client, err := abfsutil.New(ctx, endpoint, rootDir)
	if err != nil {
		t.Fatalf("abfsutil.New() = %v", err)
	}
	defer client.Close()

	ent := &abfsutil.Registration{
		Entry: merkletree.Entry{
			Name: path.Path("gen/foo.h"),
			Data: blob.NewData(nil, digest.Digest{Hash: "hash1", SizeBytes: 10}),
		},
	}
	err = client.RegisterFiles(ctx, buildDir, []*abfsutil.Registration{ent})
	if err != nil {
		t.Fatalf("RegisterFiles() = %v, want nil", err)
	}
	if ent.Err != nil {
		t.Errorf("ent.Err = %v, want nil", ent.Err)
	}
}

func TestRegisterFiles_UnknownPathInResponse(t *testing.T) {
	ctx := t.Context()
	rootDir := "/src"
	buildDir := filepath.Join(rootDir, "out/Default")

	endpoint := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]string{
			"out/Default/unrelated.h": "some error",
		}
		respBody, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(respBody)
	}))

	client, err := abfsutil.New(ctx, endpoint, rootDir)
	if err != nil {
		t.Fatalf("abfsutil.New() = %v", err)
	}
	defer client.Close()

	ent := &abfsutil.Registration{
		Entry: merkletree.Entry{
			Name: path.Path("gen/foo.h"),
			Data: blob.NewData(nil, digest.Digest{Hash: "hash1", SizeBytes: 10}),
		},
	}
	err = client.RegisterFiles(ctx, buildDir, []*abfsutil.Registration{ent})
	if err != nil {
		t.Fatalf("RegisterFiles() = %v, want nil", err)
	}
	if ent.Err != nil {
		t.Errorf("ent.Err = %v, want nil", ent.Err)
	}
}

func TestRegisterFiles_InvalidEntries(t *testing.T) {
	ctx := t.Context()
	rootDir := "/src"
	buildDir := filepath.Join(rootDir, "out/Default")

	var gotReq abfsutil.SetRBEDigestsReq

	endpoint := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &gotReq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	client, err := abfsutil.New(ctx, endpoint, rootDir)
	if err != nil {
		t.Fatalf("abfsutil.New() = %v", err)
	}
	defer client.Close()

	entOutOfDir := &abfsutil.Registration{
		Entry: merkletree.Entry{
			Name: path.Path("../../../outside.h"),
			Data: blob.NewData(nil, digest.Digest{Hash: "hash-outside", SizeBytes: 10}),
		},
	}
	entEmptyDigest := &abfsutil.Registration{
		Entry: merkletree.Entry{
			Name: path.Path("gen/empty.h"),
			Data: blob.Data{},
		},
	}
	entValid := &abfsutil.Registration{
		Entry: merkletree.Entry{
			Name: path.Path("gen/valid.h"),
			Data: blob.NewData(nil, digest.Digest{Hash: "hash-valid", SizeBytes: 30}),
		},
	}

	entries := []*abfsutil.Registration{
		nil,
		entOutOfDir,
		entEmptyDigest,
		entValid,
	}

	err = client.RegisterFiles(ctx, buildDir, entries)
	if err != nil {
		t.Fatalf("RegisterFiles() = %v, want nil", err)
	}

	if entOutOfDir.Err == nil {
		t.Errorf("entOutOfDir.Err = nil, want error")
	}
	if entEmptyDigest.Err == nil {
		t.Errorf("entEmptyDigest.Err = nil, want error")
	}
	if entValid.Err != nil {
		t.Errorf("entValid.Err = %v, want nil", entValid.Err)
	}

	wantReq := abfsutil.SetRBEDigestsReq{
		Digests: []abfsutil.RBEPathStat{
			{
				Path:   "out/Default/gen/valid.h",
				SHA256: "hash-valid",
				Size:   30,
				Mode:   uint32(syscall.S_IFREG | 0o644),
			},
		},
	}
	if diff := cmp.Diff(wantReq, gotReq); diff != "" {
		t.Errorf("SetRBEDigestsReq diff (-want +got):\n%s", diff)
	}
}

func TestRegisterFiles_HTTPError(t *testing.T) {
	ctx := t.Context()
	rootDir := "/src"
	buildDir := filepath.Join(rootDir, "out/Default")

	endpoint := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))

	client, err := abfsutil.New(ctx, endpoint, rootDir)
	if err != nil {
		t.Fatalf("abfsutil.New() = %v", err)
	}
	defer client.Close()

	ent := &abfsutil.Registration{
		Entry: merkletree.Entry{
			Name: path.Path("gen/foo.h"),
			Data: blob.NewData(nil, digest.Digest{Hash: "hash1", SizeBytes: 10}),
		},
	}
	err = client.RegisterFiles(ctx, buildDir, []*abfsutil.Registration{ent})
	if err == nil {
		t.Fatalf("RegisterFiles() = nil, want error")
	}
}

func TestRegisterFiles_InvalidJSONResponse(t *testing.T) {
	ctx := t.Context()
	rootDir := "/src"
	buildDir := filepath.Join(rootDir, "out/Default")

	endpoint := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{invalid-json"))
	}))

	client, err := abfsutil.New(ctx, endpoint, rootDir)
	if err != nil {
		t.Fatalf("abfsutil.New() = %v", err)
	}
	defer client.Close()

	ent := &abfsutil.Registration{
		Entry: merkletree.Entry{
			Name: path.Path("gen/foo.h"),
			Data: blob.NewData(nil, digest.Digest{Hash: "hash1", SizeBytes: 10}),
		},
	}
	err = client.RegisterFiles(ctx, buildDir, []*abfsutil.Registration{ent})
	if err == nil {
		t.Fatalf("RegisterFiles() = nil, want error")
	}
}

func TestRegisterFiles_NetworkError(t *testing.T) {
	ctx := t.Context()
	rootDir := "/src"
	buildDir := filepath.Join(rootDir, "out/Default")

	sockPath := filepath.Join(t.TempDir(), "abfs.sock")
	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &httptest.Server{
		Listener: lis,
		Config:   &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})},
	}
	srv.Start()
	// Close immediately so network requests will fail.
	srv.Close()

	client, err := abfsutil.New(ctx, "unix://"+sockPath, rootDir)
	if err != nil {
		t.Fatalf("abfsutil.New() = %v", err)
	}
	defer client.Close()

	ent := &abfsutil.Registration{
		Entry: merkletree.Entry{
			Name: path.Path("gen/foo.h"),
			Data: blob.NewData(nil, digest.Digest{Hash: "hash1", SizeBytes: 10}),
		},
	}
	err = client.RegisterFiles(ctx, buildDir, []*abfsutil.Registration{ent})
	if err == nil {
		t.Fatalf("RegisterFiles() = nil, want network error")
	}
}

func TestDigest(t *testing.T) {
	ctx := t.Context()
	rootDir := "/src"

	endpoint := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/mnt/get-rbe-digest" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		p := r.URL.Query().Get("path")
		switch p {
		case "out/Default/gen/foo.h":
			resp := abfsutil.RBEPathStat{
				Path:   p,
				SHA256: "hash-foo",
				Size:   100,
			}
			respBody, _ := json.Marshal(resp)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(respBody)
		case "out/Default/gen/notfound.h":
			http.Error(w, "file not found", http.StatusNotFound)
		case "out/Default/gen/invalid_json.h":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{invalid-json"))
		case "out/Default/gen/server_error.h":
			http.Error(w, "internal server error", http.StatusInternalServerError)
		default:
			http.Error(w, fmt.Sprintf("unknown path %q", p), http.StatusBadRequest)
		}
	}))

	client, err := abfsutil.New(ctx, endpoint, rootDir)
	if err != nil {
		t.Fatalf("abfsutil.New() = %v", err)
	}
	defer client.Close()

	t.Run("success", func(t *testing.T) {
		ctx := t.Context()
		d, err := client.Digest(ctx, filepath.Join(rootDir, "out/Default/gen/foo.h"))
		if err != nil {
			t.Fatalf("Digest() = %v, want nil", err)
		}
		want := digest.Digest{Hash: "hash-foo", SizeBytes: 100}
		if d != want {
			t.Errorf("Digest() = %v, want %v", d, want)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		ctx := t.Context()
		_, err := client.Digest(ctx, filepath.Join(rootDir, "out/Default/gen/notfound.h"))
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Digest() error = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("server_error", func(t *testing.T) {
		ctx := t.Context()
		_, err := client.Digest(ctx, filepath.Join(rootDir, "out/Default/gen/server_error.h"))
		if err == nil {
			t.Fatalf("Digest() = nil, want error")
		}
	})

	t.Run("invalid_json", func(t *testing.T) {
		ctx := t.Context()
		_, err := client.Digest(ctx, filepath.Join(rootDir, "out/Default/gen/invalid_json.h"))
		if err == nil {
			t.Fatalf("Digest() = nil, want error")
		}
	})

	t.Run("out_of_dir", func(t *testing.T) {
		ctx := t.Context()
		_, err := client.Digest(ctx, "/other/path.h")
		if err == nil {
			t.Fatalf("Digest() = nil, want error")
		}
	})

	t.Run("unsupported_client", func(t *testing.T) {
		ctx := t.Context()
		var nilClient *abfsutil.Client
		_, err := nilClient.Digest(ctx, filepath.Join(rootDir, "out/Default/gen/foo.h"))
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("Digest() = %v, want %v", err, errors.ErrUnsupported)
		}
	})
}

func TestNew_UnixSocket(t *testing.T) {
	tmpdir := t.TempDir()
	sockPath := filepath.Join(tmpdir, "test.sock")

	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}
	defer lis.Close()

	t.Run("abs_path", func(t *testing.T) {
		ctx := t.Context()
		client, err := abfsutil.New(ctx, "unix://"+sockPath, tmpdir)
		if err != nil {
			t.Fatalf("New() = %v", err)
		}
		defer client.Close()
	})

	t.Run("rel_path", func(t *testing.T) {
		ctx := t.Context()
		client, err := abfsutil.New(ctx, "unix://test.sock", tmpdir)
		if err != nil {
			t.Fatalf("New() = %v", err)
		}
		defer client.Close()
	})
}
