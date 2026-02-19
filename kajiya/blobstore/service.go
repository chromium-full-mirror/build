// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package blobstore implements the REAPI ContentAddressableStorage and ByteStream services.
package blobstore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.chromium.org/build/kajiya/digest"
)

const (
	// The maximum chunk size to write back to the client in Send calls.
	maxChunkSize int = 2 * 1024 * 1024
)

// Service implements the REAPI ContentAddressableStorage and ByteStream services.
type Service struct {
	repb.UnimplementedContentAddressableStorageServer
	bspb.UnimplementedByteStreamServer

	cas       *ContentAddressableStorage
	uploadDir string

	encoderPool   sync.Pool
	decoderPool   sync.Pool
	bufWriterPool sync.Pool
	bufPool       sync.Pool
}

// Register creates and registers a new Service with the given gRPC server.
// The uploadDir is created if it does not exist.
func Register(s *grpc.Server, cas *ContentAddressableStorage, uploadDir string) error {
	if uploadDir == "" {
		return fmt.Errorf("uploadDir must be set")
	}

	// Ensure that our temporary upload directory exists.
	if err := os.Mkdir(uploadDir, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}

	service := &Service{
		cas:       cas,
		uploadDir: uploadDir,
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
		bufPool: sync.Pool{
			New: func() any {
				buf := make([]byte, maxChunkSize)
				return &buf
			},
		},
	}

	bspb.RegisterByteStreamServer(s, service)
	repb.RegisterContentAddressableStorageServer(s, service)
	return nil
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
func parseReadResource(name string) (digest.Digest, repb.Compressor_Value, error) {
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
	switch fields[0] {
	case "blobs":
		if len(fields) != 3 {
			return d, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must match format {instance_name}/blobs/{hash}/{size}: %s", name)
		}
		c = repb.Compressor_IDENTITY
		hash = fields[1]
		sizeField = fields[2]
	case "compressed-blobs":
		if len(fields) != 4 {
			return d, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must match format {instance_name}/compressed-blobs/{compressor}/{uncompressed_hash}/{uncompressed_size}: %s", name)
		}
		if fields[1] != "zstd" {
			return d, c, status.Errorf(codes.InvalidArgument, "invalid compressor type, only \"zstd\" is supported: %q", fields[1])
		}
		c = repb.Compressor_ZSTD
		hash = fields[2]
		sizeField = fields[3]
	default:
		return d, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must match format {instance_name}/blobs/{hash}/{size} or {instance_name}/compressed-blobs/{compressor}/{uncompressed_hash}/{uncompressed_size}: %s", name)
	}

	size, err := strconv.ParseInt(sizeField, 10, 64)
	if err != nil {
		return d, c, status.Errorf(codes.InvalidArgument, "invalid resource name, size must be an integer: %s", sizeField)
	}
	if size < 0 {
		return d, c, status.Errorf(codes.InvalidArgument, "invalid resource name, size must be non-negative: %d", size)
	}
	d, err = digest.New(hash, size)
	if err != nil {
		return d, c, status.Errorf(codes.InvalidArgument, "invalid resource name, hash is not a valid digest: %s => %s", hash, err)
	}

	return d, c, nil
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
func parseWriteResource(name string) (digest.Digest, uuid.UUID, repb.Compressor_Value, error) {
	var d digest.Digest
	var u uuid.UUID
	var c repb.Compressor_Value

	fields := strings.Split(name, "/")

	// Strip any parts before "uploads", as they'll belong to an instance name.
	for i := range fields {
		if fields[i] == "uploads" {
			fields = fields[i:]
			break
		}
	}

	if len(fields) < 3 || fields[0] != "uploads" {
		return d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must follow format {instance_name}/uploads/{uuid}/blobs/{hash}/{size}[/{optionalmetadata}] or {instance_name}/uploads/{uuid}/compressed-blobs/{compressor}/{hash}/{size}[/{optionalmetadata}]: %s", name)
	}
	u, err := uuid.Parse(fields[1])
	if err != nil {
		return d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, second component is not a UUID: %s", fields[1])
	}

	var hash string
	var sizeField string
	switch fields[2] {
	case "blobs":
		fields = fields[3:]
		if len(fields) < 2 {
			return d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must match format {instance_name}/uploads/{uuid}/blobs/{hash}/{size}[/{optionalmetadata}]: %s", name)
		}
		c = repb.Compressor_IDENTITY
		hash = fields[0]
		sizeField = fields[1]
	case "compressed-blobs":
		fields = fields[3:]
		if len(fields) < 3 {
			return d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must match format {instance_name}/uploads/{uuid}/compressed-blobs/{compressor}/{uncompressed_hash}/{uncompressed_size}[/{optionalmetadata}]: %s", name)
		}
		if fields[0] != "zstd" {
			return d, u, c, status.Errorf(codes.InvalidArgument, "invalid compressor type, only \"zstd\" is supported: %q", fields[0])
		}
		c = repb.Compressor_ZSTD
		hash = fields[1]
		sizeField = fields[2]
	default:
		return d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, must follow format {instance_name}/uploads/{uuid}/blobs/{hash}/{size}[/{optionalmetadata}] or {instance_name}/uploads/{uuid}/compressed-blobs/{compressor}/{hash}/{size}[/{optionalmetadata}]: %s", name)
	}

	size, err := strconv.ParseInt(sizeField, 10, 64)
	if err != nil {
		return d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, size must be an integer: %s", sizeField)
	}
	if size < 0 {
		return d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, size must be non-negative: %d", size)
	}
	d, err = digest.New(hash, size)
	if err != nil {
		return d, u, c, status.Errorf(codes.InvalidArgument, "invalid resource name, hash is not a valid digest: %s => %s", hash, err)
	}

	return d, u, c, nil
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

	d, c, err := parseReadResource(request.ResourceName)
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
	if request.ReadOffset > d.Size {
		return status.Error(codes.OutOfRange, "offset is greater than the size of the file")
	}
	if shouldCompress && request.ReadLimit != 0 {
		return status.Error(codes.OutOfRange, "limit must be zero when reading compressed blob")
	}

	// Open the file and seek to the offset.
	f, err := s.cas.Open(d, request.ReadOffset, request.ReadLimit)
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

	buf             []byte
	resName         string
	expectedDigest  digest.Digest
	uploadID        uuid.UUID
	isCompressed    bool
	finishedWriting bool
	receivedBytes   int64
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
	wr, err := r.ws.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			if r.finishedWriting {
				return 0, io.EOF
			}
			return 0, io.ErrUnexpectedEOF
		}
		return 0, status.Errorf(codes.Internal, "failed to receive request from client: %v", err)
	}

	// If this is the first request from the client, we need to parse the resource name and compression algorithm.
	if r.resName == "" {
		if wr.ResourceName == "" {
			return 0, status.Error(codes.InvalidArgument, "no resource name specified")
		}
		r.resName = wr.ResourceName

		var comp repb.Compressor_Value
		r.expectedDigest, r.uploadID, comp, err = parseWriteResource(r.resName)
		if err != nil {
			return 0, err
		}
		if comp != repb.Compressor_IDENTITY && comp != repb.Compressor_ZSTD {
			return 0, status.Error(codes.InvalidArgument, "unsupported compression algorithm")
		}
		r.isCompressed = comp == repb.Compressor_ZSTD
	}

	// Validate a few things about the request.
	if wr.ResourceName != "" && wr.ResourceName != r.resName {
		return 0, status.Errorf(codes.InvalidArgument, "cannot change resource name during upload (%v => %v)", r.resName, wr.ResourceName)
	}
	if r.finishedWriting {
		return 0, status.Error(codes.InvalidArgument, "cannot write more data, last request already set finish_write=true")
	}
	if wr.WriteOffset != r.receivedBytes {
		return 0, status.Errorf(codes.InvalidArgument, "write_offset %d does not match total bytes received so far %d", wr.WriteOffset, r.receivedBytes)
	}

	r.buf = wr.Data
	r.receivedBytes += int64(len(wr.Data))
	r.finishedWriting = wr.FinishWrite

	return r.Read(p)
}

// Write implements the ByteStream.Write RPC.
func (s *Service) Write(server bspb.ByteStream_WriteServer) (err error) {
	req := &writeRequestReader{ws: server}

	defer func() {
		if err != nil {
			slog.Error("Write", "resource", req.resName, "error", err)
		} else {
			slog.Info("Write", "resource", req.resName)
		}
	}()

	// Receive the first request from the client so that we have access to the resource name etc.
	_, err = req.Read(nil)
	if err != nil {
		return err
	}

	// If the blob already exists in our CAS, tell the client and close the stream.
	if s.cas.Has(req.expectedDigest) {
		return server.SendAndClose(s.blobAlreadyExists(req.expectedDigest, req.isCompressed))
	}

	// Read the data through a zstd.Decoder if it's compressed.
	var r io.Reader = req
	if req.isCompressed {
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

	// Create a temporary file to receive the data.
	tempFile, err := digest.NewHashingFileWriter(filepath.Join(s.uploadDir, req.uploadID.String()))
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return status.Error(codes.InvalidArgument, "upload with same uuid already in progress")
		}
		return status.Errorf(codes.Internal, "could not create temporary file for upload: %v", err)
	}
	defer func() {
		if tempFile != nil {
			if err := tempFile.Close(); err != nil {
				slog.Error("failed to close temporary file", "error", err)
			}
			if err := tempFile.Delete(); err != nil {
				slog.Error("failed to delete temporary file", "error", err)
			}
			tempFile = nil
		}
	}()

	buf := *(s.bufPool.Get().(*[]byte))
	defer s.bufPool.Put(&buf)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_, werr := tempFile.Write(buf[:n])
			if werr != nil {
				return status.Errorf(codes.Internal, "failed to write to temporary file: %v", werr)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}

		// Check again if the blob already exists in our CAS, in case multiple clients are uploading the same file.
		if s.cas.Has(req.expectedDigest) {
			return server.SendAndClose(s.blobAlreadyExists(req.expectedDigest, req.isCompressed))
		}

		// If the file is already larger than the expected size, something is wrong - return an error.
		if tempFile.Size() > req.expectedDigest.Size {
			return status.Errorf(codes.InvalidArgument, "received %d bytes, more than expected %d", req.receivedBytes, req.expectedDigest.Size)
		}
	}

	if err := tempFile.Close(); err != nil {
		return status.Errorf(codes.Internal, "failed to close temporary file: %v", err)
	}

	// Check that the digests (= hash and size) match.
	d := tempFile.Digest()
	if d != req.expectedDigest {
		return status.Errorf(codes.InvalidArgument, "computed digest %v did not match expected digest %v", d, req.expectedDigest)
	}

	// Move the temporary file to the CAS.
	if err := s.cas.Adopt(req.expectedDigest, tempFile.Path()); err != nil {
		return status.Errorf(codes.Internal, "failed to move file into CAS: %v", err)
	}
	tempFile = nil

	// Send the response to the client.
	return server.SendAndClose(&bspb.WriteResponse{
		CommittedSize: d.Size,
	})
}

func (s *Service) blobAlreadyExists(d digest.Digest, isCompressed bool) *bspb.WriteResponse {
	// "The request will terminate immediately without error, and with a response whose `committed_size` is the
	// value `-1` if this is a compressed upload, or with the full size of the uploaded file if this is an
	// uncompressed upload (regardless of how much data was transmitted by the client)"
	// https://github.com/bazelbuild/remote-apis/blob/v2.3.0/build/bazel/remote/execution/v2/remote_execution.proto#L256-L265
	size := d.Size
	if isCompressed {
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

	d, _, _, err := parseWriteResource(request.ResourceName)
	if err != nil {
		return nil, err
	}

	// Check if the file exists in the CAS, if yes, the upload is complete.
	if s.cas.Has(d) {
		return &bspb.QueryWriteStatusResponse{
			CommittedSize: d.Size,
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

	// If the client explicitly specifies a DigestFunction, ensure that it's SHA256.
	if request.DigestFunction != repb.DigestFunction_UNKNOWN && request.DigestFunction != repb.DigestFunction_SHA256 {
		return nil, status.Errorf(codes.InvalidArgument, "hash function %q is not supported", request.DigestFunction.String())
	}

	// Filter the list in place so that only the missing blobs remain.
	n := 0
	for _, d := range request.BlobDigests {
		dg, err := digest.NewFromProto(d)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid digest: %v", err)
		}
		if !s.cas.Has(dg) {
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

	// If the client explicitly specifies a DigestFunction, ensure that it's SHA256.
	if request.DigestFunction != repb.DigestFunction_UNKNOWN && request.DigestFunction != repb.DigestFunction_SHA256 {
		return nil, status.Errorf(codes.InvalidArgument, "hash function %q is not supported", request.DigestFunction.String())
	}

	// Prepare a response that we can fill in.
	response := &repb.BatchUpdateBlobsResponse{
		Responses: make([]*repb.BatchUpdateBlobsResponse_Response, 0, len(request.Requests)),
	}

	// For each blob in the list, check if it exists in the CAS. If not, write it to the CAS.
	for _, blob := range request.Requests {
		// Ensure that the client didn't send compressed data.
		if blob.Compressor != repb.Compressor_IDENTITY {
			return nil, status.Error(codes.InvalidArgument, "compressed data is not supported")
		}

		// Parse the digest.
		expectedDigest, err := digest.NewFromProto(blob.Digest)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid digest: %v", err)
		}

		// Store the blob in our CAS.
		actualDigest, err := s.cas.Put(blob.Data)
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

func (s *Service) BatchReadBlobs(ctx context.Context, request *repb.BatchReadBlobsRequest) (resp *repb.BatchReadBlobsResponse, err error) {
	defer func() {
		if err != nil {
			slog.Error("BatchReadBlobs", "blobs", len(request.Digests), "error", err)
		} else {
			slog.Info("BatchReadBlobs", "blobs", len(request.Digests))
		}
	}()

	// If the client explicitly specifies a DigestFunction, ensure that it's SHA256.
	if request.DigestFunction != repb.DigestFunction_UNKNOWN && request.DigestFunction != repb.DigestFunction_SHA256 {
		return nil, status.Errorf(codes.InvalidArgument, "hash function %q is not supported", request.DigestFunction.String())
	}

	// Prepare a response that we can fill in.
	response := &repb.BatchReadBlobsResponse{
		Responses: make([]*repb.BatchReadBlobsResponse_Response, 0, len(request.Digests)),
	}

	// For each blob in the list, check if it exists in the CAS. If yes, read it from the CAS.
	for _, d := range request.Digests {
		// Parse the digest.
		dg, err := digest.NewFromProto(d)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid digest: %v", err)
		}

		// Prepare the response proto for this blob.
		r := &repb.BatchReadBlobsResponse_Response{
			Digest: d,
		}

		// Read the blob from the CAS.
		data, err := s.cas.Get(dg)
		if err != nil {
			if mbe := (&MissingBlobsError{}); errors.As(err, &mbe) {
				r.Status = status.New(codes.NotFound, "").Proto()
			} else {
				slog.Warn("failed to read blob", "digest", dg, "error", err)
				r.Status = status.New(codes.Internal, err.Error()).Proto()
			}
		} else {
			r.Data = data
			r.Status = status.New(codes.OK, "").Proto()
		}

		response.Responses = append(response.Responses, r)
	}

	// Return the response to the client.
	return response, nil
}

func (s *Service) GetTree(request *repb.GetTreeRequest, treeServer repb.ContentAddressableStorage_GetTreeServer) (err error) {
	defer func() {
		if err != nil {
			slog.Error("GetTree", "digest", request.RootDigest, "error", err)
		} else {
			slog.Info("GetTree", "digest", request.RootDigest)
		}
	}()

	// If the client explicitly specifies a DigestFunction, ensure that it's SHA256.
	if request.DigestFunction != repb.DigestFunction_UNKNOWN && request.DigestFunction != repb.DigestFunction_SHA256 {
		return status.Errorf(codes.InvalidArgument, "hash function %q is not supported", request.DigestFunction.String())
	}

	// Parse the digest.
	d, err := digest.NewFromProto(request.RootDigest)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid digest: %v", err)
	}

	// Flatten the directory tree.
	_, dirs, err := s.cas.FlattenDirectory(d)
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
