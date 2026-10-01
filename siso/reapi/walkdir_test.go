// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package reapi_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi"
	"go.chromium.org/build/siso/reapi/merkletree"
	"go.chromium.org/build/siso/reapi/reapitest"
)

// casSpy counts the CAS reads WalkDir makes.
type casSpy struct {
	reads atomic.Int32
}

func (s *casSpy) dialOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			err := invoker(ctx, method, req, reply, cc, opts...)
			if strings.HasSuffix(method, "/BatchReadBlobs") {
				s.reads.Add(1)
			}
			return err
		}),
		grpc.WithChainStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			if strings.HasSuffix(method, "/GetTree") {
				s.reads.Add(1)
			}
			return streamer(ctx, desc, cc, method, opts...)
		}),
	}
}

// walk walks the tree d and returns a summary of each directory passed
// to the WalkDir callback, keyed by its name.
func walk(ctx context.Context, cl *reapi.Client, d digest.Digest) (map[string]string, error) {
	got := make(map[string]string)
	// refs maps each directory name to the digest its parent refers to
	// it by. WalkDir visits a parent before its subdirectories.
	refs := map[string]digest.Digest{"": d}
	err := cl.WalkDir(ctx, d, func(dname string, dd digest.Digest, dir *rpb.Directory) error {
		// dd must be the digest dir is referred to by (d for the root),
		// which is not necessarily the digest of re-marshalling dir.
		if want, ok := refs[dname]; !ok || dd != want {
			return fmt.Errorf("dir %q: dd=%s; want %s", dname, dd, want)
		}
		var files, dirs, symlinks []string
		for _, file := range dir.Files {
			files = append(files, file.GetName())
		}
		for _, subdir := range dir.Directories {
			dirs = append(dirs, subdir.GetName())
			subname := subdir.GetName()
			if dname != "" {
				subname = dname + "/" + subname
			}
			refs[subname] = digest.FromProto(subdir.GetDigest())
		}
		for _, symlink := range dir.Symlinks {
			symlinks = append(symlinks, symlink.GetName())
		}
		got[dname] = fmt.Sprintf("files=%q dirs=%q symlinks=%q", files, dirs, symlinks)
		return nil
	})
	return got, err
}

func uploadTree(ctx context.Context, t *testing.T, cl *reapi.Client, files ...string) digest.Digest {
	t.Helper()
	ds := blob.NewStore()
	tree := merkletree.New(digest.SHA256, ds)
	for _, s := range files {
		tree.Set(merkletree.Entry{
			Name: path.Path(s),
			Data: blob.FromBytes(digest.SHA256, "empty", nil),
		})
	}
	d, err := tree.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n, err := cl.UploadAll(ctx, ds)
	if err != nil {
		t.Fatalf("UploadAll()=%d, %v; want _, nil", n, err)
	}
	return d
}

func TestWalkDir(t *testing.T) {
	ctx := t.Context()
	want := map[string]string{
		"":                  `files=["file1"] dirs=["subdir1" "subdir2"] symlinks=[]`,
		"subdir1":           `files=["file1"] dirs=[] symlinks=[]`,
		"subdir2":           `files=["file1"] dirs=["subdir2.1" "subdir2.2"] symlinks=[]`,
		"subdir2/subdir2.1": `files=["file1"] dirs=[] symlinks=[]`,
		"subdir2/subdir2.2": `files=["file1"] dirs=[] symlinks=[]`,
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var spy casSpy
			cl := reapitest.NewWithOption(ctx, t, &reapitest.Fake{}, reapi.Option{WalkDirStream: stream}, spy.dialOptions()...)
			d := uploadTree(ctx, t, cl,
				"file1",
				"subdir1/file1",
				"subdir2/file1",
				"subdir2/subdir2.1/file1",
				"subdir2/subdir2.2/file1")

			// The first walk reads from CAS and fills the walk cache.
			// The second is served from the cache, by digest.
			for _, pass := range []string{"fetch", "cached"} {
				before := spy.reads.Load()
				got, err := walk(ctx, cl, d)
				if err != nil {
					t.Fatalf("%s: WalkDir=%v; want nil", pass, err)
				}
				if diff := cmp.Diff(want, got); diff != "" {
					t.Errorf("%s: WalkDir diff -want +got:\n%s", pass, diff)
				}
				reads := spy.reads.Load() - before
				if (pass == "cached") != (reads == 0) {
					t.Errorf("%s: %d CAS reads", pass, reads)
				}
			}
		})
	}
}
