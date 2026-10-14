// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package upstreamexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"

	"github.com/google/uuid"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
)

// downloadOutputs walks the ActionResult and pulls every referenced blob
// into the local CAS. It also normalizes inline stdout/stderr into digest
// references and populates both output-directory digest forms (TreeDigest
// and RootDirectoryDigest) on each OutputDirectory, whichever the upstream
// sent.
//
// On return, every digest referenced by ar can be served by the local CAS.
func (e *Executor) downloadOutputs(ctx context.Context, fn digest.Function, ar *repb.ActionResult) error {
	// Normalize inline stdout/stderr into digest references so the downstream
	// code only has to consider digest fields.
	if len(ar.StdoutRaw) > 0 {
		d, err := e.cas.Put(fn, ar.StdoutRaw)
		if err != nil {
			return fmt.Errorf("put inline stdout: %w", err)
		}
		ar.StdoutDigest = d.Proto()
		ar.StdoutRaw = nil
	}
	if len(ar.StderrRaw) > 0 {
		d, err := e.cas.Put(fn, ar.StderrRaw)
		if err != nil {
			return fmt.Errorf("put inline stderr: %w", err)
		}
		ar.StderrDigest = d.Proto()
		ar.StderrRaw = nil
	}

	// Collect the blobs to fetch from upstream: output files, stdout, and
	// stderr.
	toFetch := make([]digest.Digest, 0, len(ar.OutputFiles)+2)
	addDigest := func(pd *repb.Digest) error {
		if pd == nil {
			return nil
		}
		d, err := fn.FromProto(pd)
		if err != nil {
			return err
		}
		if d == fn.Empty() {
			return nil
		}
		if e.cas.Has(fn, d) {
			return nil
		}
		toFetch = append(toFetch, d)
		return nil
	}
	for _, f := range ar.OutputFiles {
		if err := addDigest(f.Digest); err != nil {
			return fmt.Errorf("output file %q: %w", f.Path, err)
		}
	}
	if err := addDigest(ar.StdoutDigest); err != nil {
		return fmt.Errorf("stdout digest: %w", err)
	}
	if err := addDigest(ar.StderrDigest); err != nil {
		return fmt.Errorf("stderr digest: %w", err)
	}

	// Queue the Tree blob of each output directory that has a TreeDigest for
	// the same batch download. A directory with only a RootDirectoryDigest
	// needs recursive handling and is processed below.
	for _, od := range ar.OutputDirectories {
		if od.TreeDigest == nil {
			continue
		}
		td, err := fn.FromProto(od.TreeDigest)
		if err != nil {
			return fmt.Errorf("output dir %q tree digest: %w", od.Path, err)
		}
		if !e.cas.Has(fn, td) {
			toFetch = append(toFetch, td)
		}
	}
	if err := e.downloadBlobs(ctx, fn, toFetch); err != nil {
		return err
	}

	// Fill in both forms of each output directory, TreeDigest and
	// RootDirectoryDigest, whichever the upstream sent, so that a client can
	// read either. Action cache validation accepts either form. Store every
	// blob that either form references in the local CAS.
	for _, od := range ar.OutputDirectories {
		switch {
		case od.TreeDigest != nil:
			td, err := fn.FromProto(od.TreeDigest)
			if err != nil {
				return fmt.Errorf("output dir %q tree digest: %w", od.Path, err)
			}
			rootDigest, fileDigests, err := e.storeDirsFromTree(fn, td)
			if err != nil {
				return fmt.Errorf("output dir %q: %w", od.Path, err)
			}
			od.RootDirectoryDigest = rootDigest.Proto()
			if err := e.fetchMissingFiles(ctx, fn, fileDigests); err != nil {
				return fmt.Errorf("output dir %q: download files: %w", od.Path, err)
			}
		case od.RootDirectoryDigest != nil:
			// Fetch the root and every nested Directory recursively, then
			// download every referenced file. Rebuild a Tree blob and populate
			// TreeDigest so REAPI clients (e.g. Bazel) reading the cached
			// result can still walk it the standard way.
			rootDigest, err := fn.FromProto(od.RootDirectoryDigest)
			if err != nil {
				return fmt.Errorf("output dir %q root digest: %w", od.Path, err)
			}
			dirs, fileDigests, err := e.fetchDirectoryRecursive(ctx, fn, rootDigest)
			if err != nil {
				return fmt.Errorf("output dir %q: %w", od.Path, err)
			}
			treeDigest, err := e.storeTreeFromDirs(fn, dirs)
			if err != nil {
				return fmt.Errorf("output dir %q: build Tree blob: %w", od.Path, err)
			}
			od.TreeDigest = treeDigest.Proto()
			if err := e.fetchMissingFiles(ctx, fn, fileDigests); err != nil {
				return fmt.Errorf("output dir %q: download files: %w", od.Path, err)
			}
		default:
			return fmt.Errorf("output dir %q has neither TreeDigest nor RootDirectoryDigest", od.Path)
		}
	}
	return nil
}

// fetchMissingFiles downloads any digests not already present in the local CAS.
// digests is allowed to contain duplicates and fn.Empty() entries.
func (e *Executor) fetchMissingFiles(ctx context.Context, fn digest.Function, digests []digest.Digest) error {
	var missing []digest.Digest
	seen := make(map[digest.Digest]struct{}, len(digests))
	for _, d := range digests {
		if d == fn.Empty() {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		if !e.cas.Has(fn, d) {
			missing = append(missing, d)
		}
	}
	return e.downloadBlobs(ctx, fn, missing)
}

// fetchDirectoryRecursive downloads the Directory blob at root and every
// Directory transitively reachable from it, then returns:
//   - dirs: a flat slice of every Directory proto, with dirs[0] being the
//     root. Each is also stored individually in the local CAS.
//   - files: every file digest referenced anywhere in the subtree.
func (e *Executor) fetchDirectoryRecursive(ctx context.Context, fn digest.Function, root digest.Digest) (dirs []*repb.Directory, files []digest.Digest, err error) {
	// Walk the tree breadth-first so missing directories at the same depth
	// can share download requests.
	queue := []digest.Digest{root}
	seen := map[digest.Digest]struct{}{root: {}}

	for len(queue) > 0 {
		// Fetch any directory blobs at this level that aren't local yet.
		var toFetch []digest.Digest
		for _, d := range queue {
			if !e.cas.Has(fn, d) {
				toFetch = append(toFetch, d)
			}
		}
		if err := e.downloadBlobs(ctx, fn, toFetch); err != nil {
			return nil, nil, fmt.Errorf("fetch directory blobs: %w", err)
		}

		// Parse each directory at this level and queue its subdirectories for
		// the next level.
		var nextQueue []digest.Digest
		for _, d := range queue {
			data, err := e.cas.Get(fn, d)
			if err != nil {
				return nil, nil, fmt.Errorf("read directory blob %s: %w", d, err)
			}
			var dir repb.Directory
			if err := proto.Unmarshal(data, &dir); err != nil {
				return nil, nil, fmt.Errorf("unmarshal directory blob %s: %w", d, err)
			}
			dirs = append(dirs, &dir)
			for _, f := range dir.Files {
				fd, err := fn.FromProto(f.Digest)
				if err != nil {
					return nil, nil, fmt.Errorf("file %q in dir %s: %w", f.Name, d, err)
				}
				files = append(files, fd)
			}
			for _, sub := range dir.Directories {
				sd, err := fn.FromProto(sub.Digest)
				if err != nil {
					return nil, nil, fmt.Errorf("subdir %q in dir %s: %w", sub.Name, d, err)
				}
				if _, ok := seen[sd]; ok {
					continue
				}
				seen[sd] = struct{}{}
				nextQueue = append(nextQueue, sd)
			}
		}
		queue = nextQueue
	}
	return dirs, files, nil
}

// storeTreeFromDirs builds a repb.Tree from dirs (with dirs[0] as the root)
// and stores it in the local CAS. It returns the resulting Tree blob digest so
// callers can populate OutputDirectory.TreeDigest.
func (e *Executor) storeTreeFromDirs(fn digest.Function, dirs []*repb.Directory) (digest.Digest, error) {
	if len(dirs) == 0 {
		return fn.Empty(), errors.New("storeTreeFromDirs: no directories")
	}
	tree := &repb.Tree{
		Root:     dirs[0],
		Children: dirs[1:],
	}
	b, err := proto.Marshal(tree)
	if err != nil {
		return fn.Empty(), err
	}
	return e.cas.Put(fn, b)
}

// storeDirsFromTree reads the Tree blob at treeDigest from the local CAS,
// stores Tree.Root and each Tree.Children entry as individual Directory
// blobs in the local CAS, and returns the digest of the root directory plus
// every file digest referenced anywhere in the tree.
//
// The per-Directory blobs back the RootDirectoryDigest form that
// downloadOutputs populates: action cache validation and clients resolve
// that form by fetching each Directory individually.
func (e *Executor) storeDirsFromTree(fn digest.Function, treeDigest digest.Digest) (rootDigest digest.Digest, fileDigests []digest.Digest, err error) {
	// Read and decode the Tree blob, which must have a root.
	treeBytes, err := e.cas.Get(fn, treeDigest)
	if err != nil {
		return fn.Empty(), nil, fmt.Errorf("get tree blob %s: %w", treeDigest, err)
	}
	var tree repb.Tree
	if err := proto.Unmarshal(treeBytes, &tree); err != nil {
		return fn.Empty(), nil, fmt.Errorf("unmarshal tree blob %s: %w", treeDigest, err)
	}
	if tree.Root == nil {
		return fn.Empty(), nil, fmt.Errorf("tree blob %s has no root", treeDigest)
	}

	// collectFiles appends the file digests of a single Directory to
	// fileDigests.
	collectFiles := func(d *repb.Directory) error {
		for _, f := range d.Files {
			fd, err := fn.FromProto(f.Digest)
			if err != nil {
				return fmt.Errorf("file %q: %w", f.Name, err)
			}
			fileDigests = append(fileDigests, fd)
		}
		return nil
	}

	// storeDir marshals a Directory, stores it in the local CAS, and returns
	// its digest. cas.Put computes the digest from the marshaled bytes. This
	// assumes that they match the digests referenced by the parent
	// directories.
	storeDir := func(d *repb.Directory) (digest.Digest, error) {
		b, err := proto.Marshal(d)
		if err != nil {
			return fn.Empty(), fmt.Errorf("marshal directory: %w", err)
		}
		dd, err := e.cas.Put(fn, b)
		if err != nil {
			return fn.Empty(), fmt.Errorf("put directory blob: %w", err)
		}
		return dd, nil
	}

	// Store the root and every child Directory, collecting their file
	// digests.
	rootDigest, err = storeDir(tree.Root)
	if err != nil {
		return fn.Empty(), nil, err
	}
	if err := collectFiles(tree.Root); err != nil {
		return fn.Empty(), nil, err
	}
	for _, child := range tree.Children {
		if _, err := storeDir(child); err != nil {
			return fn.Empty(), nil, err
		}
		if err := collectFiles(child); err != nil {
			return fn.Empty(), nil, err
		}
	}
	return rootDigest, fileDigests, nil
}

// downloadBlobs fetches every blob in digests from upstream into the local
// CAS, in the requests that forEachBatch picks. Already-present blobs in
// digests are still fetched (the caller is expected to have filtered via
// cas.Has).
func (e *Executor) downloadBlobs(ctx context.Context, fn digest.Function, digests []digest.Digest) error {
	return e.forEachBatch(digests,
		func(batch []digest.Digest) error { return e.downloadBatch(ctx, fn, batch) },
		func(d digest.Digest) error { return e.downloadByteStream(ctx, fn, d) })
}

// downloadBatch fetches batch via a single BatchReadBlobs request and
// stores the returned bytes in the local CAS. It fails if a blob is missing
// from the response, reported as failed, or doesn't match its digest.
func (e *Executor) downloadBatch(ctx context.Context, fn digest.Function, batch []digest.Digest) error {
	// Request the blobs.
	resp, err := e.casClient.BatchReadBlobs(ctx, &repb.BatchReadBlobsRequest{
		InstanceName:   e.instanceName,
		Digests:        toProtoDigests(batch),
		DigestFunction: fn.Value(),
	})
	if err != nil {
		return fmt.Errorf("BatchReadBlobs: %w", err)
	}

	// Store each returned blob. cas.Put hashes the bytes it stores, so
	// comparing its digest with the requested one catches corrupted data.
	got := make(map[digest.Digest]bool, len(batch))
	for _, r := range resp.Responses {
		if r.Status != nil && codes.Code(r.Status.Code) != codes.OK {
			return fmt.Errorf("BatchReadBlobs blob %s/%d: %s",
				r.Digest.GetHash(), r.Digest.GetSizeBytes(), r.Status.Message)
		}
		want, err := fn.FromProto(r.Digest)
		if err != nil {
			return fmt.Errorf("BatchReadBlobs returned invalid digest: %w", err)
		}
		actual, err := e.cas.Put(fn, r.Data)
		if err != nil {
			return fmt.Errorf("put blob %s into local CAS: %w", want, err)
		}
		if actual != want {
			return fmt.Errorf("BatchReadBlobs returned wrong bytes for %s: got digest %s", want, actual)
		}
		got[want] = true
	}

	// Check that the response covered the whole batch.
	for _, d := range batch {
		if !got[d] {
			return fmt.Errorf("BatchReadBlobs did not return blob %s", d)
		}
	}
	return nil
}

// downloadByteStream fetches a single blob via ByteStream.Read and stores it
// in the local CAS. It fails if the upstream returns more or fewer bytes than
// the digest's size or bytes that don't match the digest, and then leaves
// nothing behind in the local CAS. A concurrent download of the same blob
// may store it first, which counts as success.
func (e *Executor) downloadByteStream(ctx context.Context, fn digest.Function, d digest.Digest) error {
	// Open the download stream. Its context is canceled on return, which
	// releases the stream if an error leaves it open. path.Join drops an
	// empty instance name and digest function segment.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	resourceName := path.Join(e.instanceName, "blobs", fn.ResourceNameSegment(), d.Hash, strconv.FormatInt(d.SizeBytes, 10))
	stream, err := e.bsClient.Read(ctx, &bspb.ReadRequest{ResourceName: resourceName})
	if err != nil {
		return fmt.Errorf("ByteStream.Read open %s: %w", d, err)
	}

	// Stage the blob in the local CAS, which checks its size and digest.
	w, err := e.cas.NewUploadWriter(fn, d, uuid.New())
	if errors.Is(err, blobstore.ErrBlobExists) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stage blob %s: %w", d, err)
	}

	// Copy the stream into it. A failed Write removes the staging file
	// itself, and a failed Close does too.
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = w.Close()
			return fmt.Errorf("ByteStream.Read recv %s: %w", d, err)
		}
		if _, err := w.Write(resp.Data); errors.Is(err, blobstore.ErrBlobExists) {
			return nil
		} else if err != nil {
			return fmt.Errorf("ByteStream.Read %s: %w", d, err)
		}
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("ByteStream.Read %s: %w", d, err)
	}
	return nil
}
