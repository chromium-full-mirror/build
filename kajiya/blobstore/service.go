// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package blobstore implements the REAPI ContentAddressableStorage and ByteStream services.
package blobstore

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/server"
)

const (
	// The maximum chunk size to write back to the client in Send calls.
	maxChunkSize int = 2 * 1024 * 1024
)

// Service implements the REAPI ContentAddressableStorage and ByteStream services.
type Service struct {
	repb.UnimplementedContentAddressableStorageServer
	bspb.UnimplementedByteStreamServer

	cas *ContentAddressableStorage

	config server.Config

	encoderPool   sync.Pool
	decoderPool   sync.Pool
	bufWriterPool sync.Pool
}

// Register creates and registers a new Service with the given gRPC server.
func Register(s *grpc.Server, cas *ContentAddressableStorage, cfg server.Config) {
	service := &Service{
		cas:    cas,
		config: cfg,
		encoderPool: sync.Pool{
			New: func() any {
				e, err := zstd.NewWriter(nil)
				if err != nil {
					panic(err)
				}
				return e
			},
		},
		decoderPool: sync.Pool{
			New: func() any {
				d, err := zstd.NewReader(nil)
				if err != nil {
					panic(err)
				}
				return d
			},
		},
		bufWriterPool: sync.Pool{
			New: func() any {
				return bufio.NewWriterSize(nil, maxChunkSize)
			},
		},
	}

	bspb.RegisterByteStreamServer(s, service)
	repb.RegisterContentAddressableStorageServer(s, service)
}

// parseReadResource parses a ReadRequest.ResourceName and returns the validated Digest and the
// compressor. There is a difference on the resource name form between uncompressed data and
// compressed data. For uncompressed data, it is in the following form:
//
// `{instance_name}/blobs/{hash}/{size}`
//
// For compressed data, it is in the following form:
//
// `{instance_name}/compressed-blobs/{compressor}/{uncompressed_hash}/{uncompressed_size}`
func (s *Service) parseReadResource(name string) (digest.Function, digest.Digest, repb.Compressor_Value, error) {
	var fn digest.Function
	var d digest.Digest

	fields := strings.Split(name, "/")

	// Strip any parts before "blobs"/"compressed-blobs", as they'll belong to an instance name.
	for i := range fields {
		if fields[i] == "blobs" || fields[i] == "compressed-blobs" {
			fields = fields[i:]
			break
		}
	}

	var c repb.Compressor_Value
	var hash string
	var sizeField string
	var err error
	switch fields[0] {
	case "blobs":
		// {instance_name}/blobs/[{digest_function}/]{hash}/{size}
		c = repb.Compressor_IDENTITY
		var rest []string
		fn, rest, err = stripDigestFunction(fields[1:])
		if err != nil {
			return fn, d, c, err
		}
		if len(rest) != 2 {
			return fn, d, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must match format {instance_name}/blobs/[{digest_function}/]{hash}/{size}: %s", name)
		}
		hash = rest[0]
		sizeField = rest[1]
	case "compressed-blobs":
		// {instance_name}/compressed-blobs/{compressor}/[{digest_function}/]{hash}/{size}
		if len(fields) < 2 || fields[1] != "zstd" {
			if len(fields) < 2 {
				return fn, d, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must match format {instance_name}/compressed-blobs/{compressor}/[{digest_function}/]{uncompressed_hash}/{uncompressed_size}: %s", name)
			}
			return fn, d, c, status.Errorf(codes.InvalidArgument, "invalid compressor type, only \"zstd\" is supported: %q", fields[1])
		}
		c = repb.Compressor_ZSTD
		var rest []string
		fn, rest, err = stripDigestFunction(fields[2:])
		if err != nil {
			return fn, d, c, err
		}
		if len(rest) != 2 {
			return fn, d, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must match format {instance_name}/compressed-blobs/{compressor}/[{digest_function}/]{uncompressed_hash}/{uncompressed_size}: %s", name)
		}
		hash = rest[0]
		sizeField = rest[1]
	default:
		return fn, d, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must match format {instance_name}/blobs/[{digest_function}/]{hash}/{size} or {instance_name}/compressed-blobs/{compressor}/[{digest_function}/]{uncompressed_hash}/{uncompressed_size}: %s", name)
	}

	size, err := strconv.ParseInt(sizeField, 10, 64)
	if err != nil {
		return fn, d, c, status.Errorf(codes.InvalidArgument, "invalid resource name, size must be an integer: %s", sizeField)
	}
	if size < 0 {
		return fn, d, c, status.Errorf(codes.InvalidArgument, "invalid resource name, size must be non-negative: %d", size)
	}
	// An omitted {digest_function} segment is inferred from the hash length
	// and the advertised functions; an explicit one must be advertised.
	fn, err = s.config.ResolveResourceNameFunction(fn, len(hash))
	if err != nil {
		return fn, d, c, err
	}
	d, err = fn.Validate(hash, size)
	if err != nil {
		return fn, d, c, status.Errorf(codes.InvalidArgument, "invalid resource name, hash is not a valid digest: %s => %s", hash, err)
	}

	return fn, d, c, nil
}

// stripDigestFunction returns the digest function encoded as the leading field
// of a resource name (if present) and the remaining fields. SHA-256 omits the
// segment, so an absent function yields the zero Function (inferred from the
// hash length by the caller). A digest-function name is never a valid hash, so
// this is unambiguous.
func stripDigestFunction(fields []string) (digest.Function, []string, error) {
	if len(fields) > 0 {
		fn, recognized, err := digest.FunctionByName(fields[0])
		if err != nil {
			return fn, fields, status.Errorf(codes.InvalidArgument, "unsupported digest function in resource name: %q", fields[0])
		}
		if recognized {
			return fn, fields[1:], nil
		}
	}
	return digest.Function{}, fields, nil
}

// parseWriteResource parses a WriteRequest.ResourceName and returns the validated Digest and upload ID and compressor.
// There is a difference on the resource name form between uncompressed data and compressed data.
// For uncompressed data, it is in the following form:
//
// `{instance_name}/uploads/{uuid}/blobs/{hash}/{size}[/{optionalmetadata}]`
//
// For compressed data, it is in the following form:
//
// `{instance_name}/uploads/{uuid}/compressed-blobs/{compressor}/{uncompressed_hash}/{uncompressed_size}[/{optionalmetadata}]`
func (s *Service) parseWriteResource(name string) (digest.Function, digest.Digest, uuid.UUID, repb.Compressor_Value, error) {
	var fn digest.Function
	var d digest.Digest
	var u uuid.UUID
	var c repb.Compressor_Value

	if name == "" {
		return fn, d, u, c, status.Error(codes.InvalidArgument, "resource name is empty")
	}

	fields := strings.Split(name, "/")

	// Strip any parts before "uploads", as they'll belong to an instance name.
	for i := range fields {
		if fields[i] == "uploads" {
			fields = fields[i:]
			break
		}
	}

	if len(fields) < 3 || fields[0] != "uploads" {
		return fn, d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must follow format {instance_name}/uploads/{uuid}/blobs/{hash}/{size}[/{optionalmetadata}] or {instance_name}/uploads/{uuid}/compressed-blobs/{compressor}/{hash}/{size}[/{optionalmetadata}]: %s", name)
	}
	u, err := uuid.Parse(fields[1])
	if err != nil {
		return fn, d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, second component is not a UUID: %s", fields[1])
	}

	var hash string
	var sizeField string
	switch fields[2] {
	case "blobs":
		// .../uploads/{uuid}/blobs/[{digest_function}/]{hash}/{size}[/{optionalmetadata}]
		c = repb.Compressor_IDENTITY
		var rest []string
		fn, rest, err = stripDigestFunction(fields[3:])
		if err != nil {
			return fn, d, u, c, err
		}
		if len(rest) < 2 {
			return fn, d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must match format {instance_name}/uploads/{uuid}/blobs/[{digest_function}/]{hash}/{size}[/{optionalmetadata}]: %s", name)
		}
		hash = rest[0]
		sizeField = rest[1]
	case "compressed-blobs":
		// .../uploads/{uuid}/compressed-blobs/{compressor}/[{digest_function}/]{hash}/{size}[/{optionalmetadata}]
		tail := fields[3:]
		if len(tail) < 1 || tail[0] != "zstd" {
			if len(tail) < 1 {
				return fn, d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must match format {instance_name}/uploads/{uuid}/compressed-blobs/{compressor}/[{digest_function}/]{uncompressed_hash}/{uncompressed_size}[/{optionalmetadata}]: %s", name)
			}
			return fn, d, u, c, status.Errorf(codes.InvalidArgument, "invalid compressor type, only \"zstd\" is supported: %q", tail[0])
		}
		c = repb.Compressor_ZSTD
		var rest []string
		fn, rest, err = stripDigestFunction(tail[1:])
		if err != nil {
			return fn, d, u, c, err
		}
		if len(rest) < 2 {
			return fn, d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must match format {instance_name}/uploads/{uuid}/compressed-blobs/{compressor}/[{digest_function}/]{uncompressed_hash}/{uncompressed_size}[/{optionalmetadata}]: %s", name)
		}
		hash = rest[0]
		sizeField = rest[1]
	default:
		return fn, d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must follow format {instance_name}/uploads/{uuid}/blobs/[{digest_function}/]{hash}/{size}[/{optionalmetadata}] or {instance_name}/uploads/{uuid}/compressed-blobs/{compressor}/[{digest_function}/]{hash}/{size}[/{optionalmetadata}]: %s", name)
	}

	size, err := strconv.ParseInt(sizeField, 10, 64)
	if err != nil {
		return fn, d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, size must be an integer: %s", sizeField)
	}
	if size < 0 {
		return fn, d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, size must be non-negative: %d", size)
	}
	// An omitted {digest_function} segment is inferred from the hash length
	// and the advertised functions; an explicit one must be advertised.
	fn, err = s.config.ResolveResourceNameFunction(fn, len(hash))
	if err != nil {
		return fn, d, u, c, err
	}
	d, err = fn.Validate(hash, size)
	if err != nil {
		return fn, d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, hash is not a valid digest: %s => %s", hash, err)
	}

	return fn, d, u, c, nil
}

// readResponseWriter sends bytes written to it to the client by wrapping them in ByteStream.ReadResponse messages.
type readResponseWriter struct {
	rs bspb.ByteStream_ReadServer
}

// Write sends the provided byte slice to the client wrapped in one or more ByteStream.ReadResponse messages.
func (r *readResponseWriter) Write(p []byte) (n int, err error) {
	for len(p) > 0 {
		chunkSize := min(len(p), maxChunkSize)
		if err := r.rs.Send(&bspb.ReadResponse{
			Data: p[:chunkSize],
		}); err != nil {
			return n, status.Errorf(codes.Internal, "failed to send data to client: %v", err)
		}
		n += chunkSize
		p = p[chunkSize:]
	}
	return n, nil
}

// Read implements the ByteStream.Read RPC.
func (s *Service) Read(request *bspb.ReadRequest, server bspb.ByteStream_ReadServer) (err error) {
	defer func() {
		if err != nil {
			slog.Error("Read", "resource", request.ResourceName, "error", err)
		} else {
			slog.Info("Read", "resource", request.ResourceName)
		}
	}()

	fn, d, c, err := s.parseReadResource(request.ResourceName)
	if err != nil {
		return err
	}

	if c != repb.Compressor_IDENTITY && c != repb.Compressor_ZSTD {
		return status.Error(codes.InvalidArgument, "unsupported compression algorithm")
	}
	shouldCompress := c == repb.Compressor_ZSTD

	// A `read_offset` that is negative or greater than the size of the resource
	// will cause an `OUT_OF_RANGE` error.
	if request.ReadOffset < 0 {
		return status.Error(codes.OutOfRange, "offset is negative")
	}
	if request.ReadOffset > d.SizeBytes {
		return status.Error(codes.OutOfRange, "offset is greater than the size of the file")
	}
	if shouldCompress && request.ReadLimit != 0 {
		return status.Error(codes.OutOfRange, "limit must be zero when reading compressed blob")
	}

	// Open the file and seek to the offset.
	f, err := s.cas.Open(fn, d, request.ReadOffset, request.ReadLimit)
	if err != nil {
		var mbe *MissingBlobsError
		if errors.As(err, &mbe) {
			return status.Errorf(codes.NotFound, "blob not found: %v", err)
		}
		return status.Errorf(codes.Internal, "failed to open file: %v", err)
	}
	defer func() {
		if e := f.Close(); e != nil && err == nil {
			err = status.Errorf(codes.Internal, "failed to close file: %v", err)
		}
	}()

	rrw := &readResponseWriter{rs: server}

	bufw := s.bufWriterPool.Get().(*bufio.Writer)
	bufw.Reset(rrw)
	defer func() {
		if e := bufw.Flush(); e != nil && err == nil {
			err = status.Errorf(codes.Internal, "failed to flush buffer: %v", err)
		}
		bufw.Reset(nil)
		s.bufWriterPool.Put(bufw)
	}()

	var dst io.Writer = bufw
	if shouldCompress {
		encoder := s.encoderPool.Get().(*zstd.Encoder)
		encoder.Reset(bufw)
		defer func() {
			if e := encoder.Close(); e != nil && err == nil {
				err = status.Errorf(codes.Internal, "failed to close encoder: %v", err)
			}
			s.encoderPool.Put(encoder)
		}()
		dst = encoder
	}

	if _, err := io.Copy(dst, f); err != nil {
		return status.Errorf(codes.Internal, "failed to copy data: %v", err)
	}

	return nil
}

// writeRequestReader reads the bytes uploaded by a client over multiple WriteRequest messages during a ByteStream.Write
// RPC as a continuous stream.
type writeRequestReader struct {
	ws bspb.ByteStream_WriteServer

	buf            []byte
	resName        string
	fn             digest.Function
	expectedDigest digest.Digest
	uploadID       uuid.UUID
	compressor     repb.Compressor_Value
	writeOffset    int64
	finishWrite    bool
}

func (s *Service) newWriteRequestReader(ws bspb.ByteStream_WriteServer) (*writeRequestReader, error) {
	req, err := ws.Recv()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to receive initial request from client: %v", err)
	}

	r := writeRequestReader{
		ws:          ws,
		buf:         req.Data,
		resName:     req.ResourceName,
		writeOffset: req.WriteOffset,
		finishWrite: req.FinishWrite,
	}

	r.fn, r.expectedDigest, r.uploadID, r.compressor, err = s.parseWriteResource(r.resName)
	if err != nil {
		return nil, err
	}
	if r.writeOffset < 0 {
		return nil, status.Error(codes.InvalidArgument, "write_offset must be non-negative")
	} else if r.writeOffset > 0 {
		return nil, status.Error(codes.Unimplemented, "initial write_offset > 0 is not supported")
	}
	if r.compressor != repb.Compressor_IDENTITY && r.compressor != repb.Compressor_ZSTD {
		return nil, status.Error(codes.InvalidArgument, "unsupported compression algorithm")
	}

	r.writeOffset += int64(len(req.Data))

	return &r, nil
}

func (r *writeRequestReader) recv() ([]byte, error) {
	// Receive the next request from the client.
	req, err := r.ws.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			if r.finishWrite {
				return nil, io.EOF
			}
			return nil, io.ErrUnexpectedEOF
		}
		return nil, status.Errorf(codes.Internal, "failed to receive request from client: %v", err)
	}

	if req.ResourceName != "" && req.ResourceName != r.resName {
		return nil, status.Errorf(codes.InvalidArgument, "cannot change resource name during upload (%v => %v)", r.resName, req.ResourceName)
	}

	if r.finishWrite {
		return nil, status.Error(codes.InvalidArgument, "cannot write more data, last request already set finish_write=true")
	}
	r.finishWrite = req.FinishWrite

	if req.WriteOffset != r.writeOffset {
		return nil, status.Errorf(codes.InvalidArgument, "wrong write_offset: got %d, expected %d", req.WriteOffset, r.writeOffset)
	}
	r.writeOffset += int64(len(req.Data))

	return req.Data, nil
}

// Read reads bytes uploaded by the client during the ByteStream.Write RPC.
// Returns the number of bytes read and any error encountered, io.EOF if the upload is complete or io.ErrUnexpectedEOF
// if the client disconnected before the upload was complete.
func (r *writeRequestReader) Read(p []byte) (n int, err error) {
	// If we still have data from a previous request, use it up first.
	if len(r.buf) > 0 {
		n = copy(p, r.buf)
		r.buf = r.buf[n:]
		return n, nil
	}

	// Receive the next request from the client.
	if r.buf, err = r.recv(); err != nil {
		return 0, err
	}
	return r.Read(p)
}

func (r *writeRequestReader) WriteTo(w io.Writer) (n int64, err error) {
	for {
		if len(r.buf) > 0 {
			nw, err := w.Write(r.buf)
			n += int64(nw)
			if err != nil {
				if errors.Is(err, ErrBlobExists) {
					return n, ErrBlobExists
				}
				return n, status.Errorf(codes.Internal, "failed to write data: %v", err)
			}
		}

		r.buf, err = r.recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return n, nil
			}
			return n, err
		}
	}
}

// Write implements the ByteStream.Write RPC.
func (s *Service) Write(server bspb.ByteStream_WriteServer) (err error) {
	req, err := s.newWriteRequestReader(server)
	if err != nil {
		slog.Error("Write", "error", err)
		return err
	}
	defer func() {
		if err != nil {
			slog.Error("Write", "resource", req.resName, "error", err)
		} else {
			slog.Info("Write", "resource", req.resName)
		}
	}()

	// Create an UploadWriter for the blob that will store it in the CAS.
	uw, err := s.cas.NewUploadWriter(req.fn, req.expectedDigest, req.uploadID)
	if err != nil {
		if errors.Is(err, ErrBlobExists) {
			return server.SendAndClose(blobAlreadyExists(req.expectedDigest, req.compressor))
		} else if errors.Is(err, fs.ErrExist) {
			return status.Error(codes.InvalidArgument, "upload with same uuid already in progress")
		}
		return status.Errorf(codes.Internal, "could not create temporary file for upload: %v", err)
	}

	// Read the data through a zstd.Decoder if it's compressed.
	var r io.WriterTo = req
	if req.compressor == repb.Compressor_ZSTD {
		decoder := s.decoderPool.Get().(*zstd.Decoder)
		if err := decoder.Reset(req); err != nil {
			return status.Errorf(codes.Internal, "failed to reset decoder: %v", err)
		}
		defer func() {
			_ = decoder.Reset(nil)
			s.decoderPool.Put(decoder)
		}()
		r = decoder
	}

	// We don't need to check the number of written bytes vs. the expected count,
	// because `uw.Close()` does this during verification of the digest already.
	if _, err = r.WriteTo(uw); err != nil {
		if errors.Is(err, ErrBlobExists) {
			return server.SendAndClose(blobAlreadyExists(req.expectedDigest, req.compressor))
		}
		return status.Errorf(codes.Internal, "write during upload failed: %v", err)
	}

	if err := uw.Close(); err != nil {
		var dme *DigestMismatchError
		if errors.As(err, &dme) {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		return status.Errorf(codes.Internal, "upload failed: %v", err)
	}
	uw = nil

	// Send the response to the client.
	return server.SendAndClose(&bspb.WriteResponse{
		CommittedSize: req.expectedDigest.SizeBytes,
	})
}

func blobAlreadyExists(d digest.Digest, compressor repb.Compressor_Value) *bspb.WriteResponse {
	// "The request will terminate immediately without error, and with a response whose `committed_size` is the
	// value `-1` if this is a compressed upload, or with the full size of the uploaded file if this is an
	// uncompressed upload (regardless of how much data was transmitted by the client)"
	// https://github.com/bazelbuild/remote-apis/blob/v2.3.0/build/bazel/remote/execution/v2/remote_execution.proto#L256-L265
	size := d.SizeBytes
	if compressor != repb.Compressor_IDENTITY {
		size = -1
	}
	return &bspb.WriteResponse{
		CommittedSize: size,
	}
}

// QueryWriteStatus implements the ByteStream.QueryWriteStatus RPC.
func (s *Service) QueryWriteStatus(ctx context.Context, request *bspb.QueryWriteStatusRequest) (resp *bspb.QueryWriteStatusResponse, err error) {
	defer func() {
		if err != nil {
			slog.Error("QueryWriteStatus", "resource", request.ResourceName, "error", err)
		} else {
			slog.Info("QueryWriteStatus", "resource", request.ResourceName)
		}
	}()

	fn, d, _, _, err := s.parseWriteResource(request.ResourceName)
	if err != nil {
		return nil, err
	}

	// Check if the file exists in the CAS, if yes, the upload is complete.
	if s.cas.Has(fn, d) {
		return &bspb.QueryWriteStatusResponse{
			CommittedSize: d.SizeBytes,
			Complete:      true,
		}, nil
	}

	// We don't support resuming uploads yet, so just always return that we don't have any data.
	return &bspb.QueryWriteStatusResponse{
		CommittedSize: 0,
		Complete:      false,
	}, nil
}

// FindMissingBlobs implements the ContentAddressableStorage.FindMissingBlobs RPC.
func (s *Service) FindMissingBlobs(ctx context.Context, request *repb.FindMissingBlobsRequest) (resp *repb.FindMissingBlobsResponse, err error) {
	defer func() {
		if err != nil {
			slog.Error("FindMissingBlobs", "blobs", len(request.BlobDigests), "error", err)
		} else {
			slog.Info("FindMissingBlobs", "blobs", len(request.BlobDigests), "missing", len(resp.MissingBlobDigests))
		}
	}()

	// Resolve the request-level digest function once; per-digest inference
	// from the hash length only remains for UNKNOWN.
	reqFn, err := s.config.ResolveFunction(request.DigestFunction)
	if err != nil {
		return nil, err
	}

	// Filter the list in place so that only the missing blobs remain.
	n := 0
	for _, d := range request.BlobDigests {
		fn, dg, err := s.config.ResolveWith(reqFn, d)
		if err != nil {
			return nil, err
		}
		if !s.cas.Has(fn, dg) {
			request.BlobDigests[n] = d
			n++
		}
	}
	request.BlobDigests = request.BlobDigests[:n]

	// Return the list of missing blobs to the client.
	return &repb.FindMissingBlobsResponse{
		MissingBlobDigests: request.BlobDigests,
	}, nil
}

// BatchUpdateBlobs implements the ContentAddressableStorage.BatchUpdateBlobs RPC.
func (s *Service) BatchUpdateBlobs(ctx context.Context, request *repb.BatchUpdateBlobsRequest) (resp *repb.BatchUpdateBlobsResponse, err error) {
	defer func() {
		if err != nil {
			slog.Error("BatchUpdateBlobs", "blobs", len(request.Requests), "error", err)
		} else {
			slog.Info("BatchUpdateBlobs", "blobs", len(request.Requests))
		}
	}()

	// Resolve the request-level digest function once. It may be UNKNOWN even
	// for a spec-compliant SHA-1 request; the function is then inferred per
	// digest from the hash length.
	reqFn, err := s.config.ResolveFunction(request.DigestFunction)
	if err != nil {
		return nil, err
	}

	// Enforce the max batch total size limit.
	if s.config.MaxBatchTotalSizeBytes > 0 {
		var totalSize int64
		for _, blob := range request.Requests {
			totalSize += blob.Digest.SizeBytes
		}
		if totalSize > s.config.MaxBatchTotalSizeBytes {
			return nil, status.Errorf(codes.InvalidArgument, "total batch size %d exceeds the maximum allowed %d bytes", totalSize, s.config.MaxBatchTotalSizeBytes)
		}
	}

	// Prepare a response that we can fill in.
	response := &repb.BatchUpdateBlobsResponse{
		Responses: make([]*repb.BatchUpdateBlobsResponse_Response, 0, len(request.Requests)),
	}

	// For each blob in the list, check if it exists in the CAS. If not, write it to the CAS.
	for _, blob := range request.Requests {
		var data []byte

		switch blob.Compressor {
		case repb.Compressor_IDENTITY:
			data = blob.Data
		case repb.Compressor_ZSTD:
			decoder := s.decoderPool.Get().(*zstd.Decoder)
			data, err = decoder.DecodeAll(blob.Data, nil)
			s.decoderPool.Put(decoder)
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "failed to decompress blob: %v", err)
			}
		default:
			return nil, status.Error(codes.InvalidArgument, "unsupported compression algorithm")
		}

		// Parse the digest with the request's function.
		fn, expectedDigest, err := s.config.ResolveWith(reqFn, blob.Digest)
		if err != nil {
			return nil, err
		}

		actualDigest, err := s.cas.Put(fn, data)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "could not store blob in CAS: %v", err)
		}

		// Check that the calculated digest matches the data.
		if actualDigest != expectedDigest {
			return nil, status.Errorf(codes.InvalidArgument, "digest does not match data (expected %s, actual %s)", expectedDigest, actualDigest)
		}

		// Add the response to the list.
		response.Responses = append(response.Responses, &repb.BatchUpdateBlobsResponse_Response{
			Digest: blob.Digest,
			Status: status.New(codes.OK, "").Proto(),
		})
	}

	// Return the response to the client.
	return response, nil
}

// BatchReadBlobs implements the ContentAddressableStorage.BatchReadBlobs RPC.
func (s *Service) BatchReadBlobs(ctx context.Context, request *repb.BatchReadBlobsRequest) (resp *repb.BatchReadBlobsResponse, err error) {
	defer func() {
		if err != nil {
			slog.Error("BatchReadBlobs", "blobs", len(request.Digests), "error", err)
		} else {
			slog.Info("BatchReadBlobs", "blobs", len(request.Digests))
		}
	}()

	// Resolve the request-level digest function once; per-digest inference
	// from the hash length only remains for UNKNOWN.
	reqFn, err := s.config.ResolveFunction(request.DigestFunction)
	if err != nil {
		return nil, err
	}

	// Enforce the max batch total size limit.
	if s.config.MaxBatchTotalSizeBytes > 0 {
		var totalSize int64
		for _, d := range request.Digests {
			totalSize += d.SizeBytes
		}
		if totalSize > s.config.MaxBatchTotalSizeBytes {
			return nil, status.Errorf(codes.InvalidArgument, "total batch size %d exceeds the maximum allowed %d bytes", totalSize, s.config.MaxBatchTotalSizeBytes)
		}
	}

	shouldCompress := slices.Contains(request.AcceptableCompressors, repb.Compressor_ZSTD)

	// Prepare a response that we can fill in.
	response := &repb.BatchReadBlobsResponse{
		Responses: make([]*repb.BatchReadBlobsResponse_Response, 0, len(request.Digests)),
	}

	// For each blob in the list, check if it exists in the CAS. If yes, read it from the CAS.
	for _, d := range request.Digests {
		// Parse the digest with the request's function.
		fn, dg, err := s.config.ResolveWith(reqFn, d)
		if err != nil {
			return nil, err
		}

		// Prepare the response proto for this blob.
		r := &repb.BatchReadBlobsResponse_Response{
			Digest: d,
		}

		// Read the blob from the CAS.
		data, err := s.cas.Get(fn, dg)
		if err != nil {
			if mbe := (&MissingBlobsError{}); errors.As(err, &mbe) {
				r.Status = status.New(codes.NotFound, "").Proto()
			} else {
				slog.Warn("failed to read blob", "digest", dg, "error", err)
				r.Status = status.New(codes.Internal, err.Error()).Proto()
			}
		} else {
			r.Status = status.New(codes.OK, "").Proto()
			if shouldCompress {
				r.Compressor = repb.Compressor_ZSTD
				encoder := s.encoderPool.Get().(*zstd.Encoder)
				r.Data = encoder.EncodeAll(data, nil)
				s.encoderPool.Put(encoder)
			} else {
				r.Compressor = repb.Compressor_IDENTITY
				r.Data = data
			}
		}

		response.Responses = append(response.Responses, r)
	}

	// Return the response to the client.
	return response, nil
}

// GetTree implements the ContentAddressableStorage.GetTree RPC.
func (s *Service) GetTree(request *repb.GetTreeRequest, treeServer repb.ContentAddressableStorage_GetTreeServer) (err error) {
	defer func() {
		if err != nil {
			slog.Error("GetTree", "digest", request.RootDigest, "error", err)
		} else {
			slog.Info("GetTree", "digest", request.RootDigest)
		}
	}()

	// Resolve the digest function and parse the digest.
	fn, d, err := s.config.ResolveDigest(request.DigestFunction, request.RootDigest)
	if err != nil {
		return err
	}

	// Flatten the directory tree.
	_, dirs, err := s.cas.FlattenDirectory(fn, d)
	if err != nil {
		var mbe *MissingBlobsError
		if errors.As(err, &mbe) {
			return status.Errorf(codes.NotFound, "root directory not found: %v", err)
		}
		return err
	}

	// Prepare a response that we can fill in.
	response := &repb.GetTreeResponse{
		Directories: dirs,
	}

	// Send the tree to the client.
	return treeServer.Send(response)
}
