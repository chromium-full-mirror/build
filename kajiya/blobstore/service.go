// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package blobstore implements the REAPI ContentAddressableStorage and ByteStream services.
package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	bspb "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// The maximum chunk size to write back to the client in Send calls.
	maxChunkSize int64 = 2 * 1024 * 1024
)

// Service implements the REAPI ContentAddressableStorage and ByteStream services.
type Service struct {
	repb.UnimplementedContentAddressableStorageServer
	bspb.UnimplementedByteStreamServer

	cas       *ContentAddressableStorage
	uploadDir string
}

// Register creates and registers a new Service with the given gRPC server.
// The dataDir is created if it does not exist.
func Register(s *grpc.Server, cas *ContentAddressableStorage, dataDir string) error {
	service, err := NewService(cas, dataDir)
	if err != nil {
		return err
	}
	bspb.RegisterByteStreamServer(s, service)
	repb.RegisterContentAddressableStorageServer(s, service)
	return nil
}

// NewService creates a new Service.
func NewService(cas *ContentAddressableStorage, uploadDir string) (*Service, error) {
	if uploadDir == "" {
		return nil, errors.New("uploadDir must be set")
	}

	// Ensure that our temporary upload directory exists.
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return nil, err
	}

	return &Service{
		cas:       cas,
		uploadDir: uploadDir,
	}, nil
}

// parseReadResource parses a ReadRequest.ResourceName and returns the validated Digest and the compressor.
// There is a difference on the resource name form between uncompressed data and compressed data.
// For uncompressed data, it is in the following form:
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

// Read implements the ByteStream.Read RPC.
func (s *Service) Read(request *bspb.ReadRequest, server bspb.ByteStream_ReadServer) error {
	err := s.read(request, server)
	if err != nil {
		log.Printf("🚨 Read(%v) => Error: %v", request.ResourceName, err)
	} else {
		log.Printf("✅ Read(%v) => OK", request.ResourceName)
	}
	return err
}

func (s *Service) read(request *bspb.ReadRequest, server bspb.ByteStream_ReadServer) error {
	d, c, err := parseReadResource(request.ResourceName)
	if err != nil {
		return err
	}

	// A `read_offset` that is negative or greater than the size of the resource
	// will cause an `OUT_OF_RANGE` error.
	if request.ReadOffset < 0 {
		return status.Error(codes.OutOfRange, "offset is negative")
	}
	if request.ReadOffset > d.Size {
		return status.Error(codes.OutOfRange, "offset is greater than the size of the file")
	}
	if c != repb.Compressor_IDENTITY && request.ReadLimit != 0 {
		return status.Error(codes.InvalidArgument, "read_limit must be zero when reading compressed blob")
	}

	// Open the file and seek to the offset.
	f, err := s.cas.Open(d, request.ReadOffset, request.ReadLimit)
	if err != nil {
		var mbe *MissingBlobsError
		if !errors.As(err, &mbe) {
			return status.Errorf(codes.NotFound, "blob not found: %v", err)
		}
		return status.Errorf(codes.Internal, "failed to open file: %v", err)
	}
	defer func() {
		// Safe to ignore, because we're only reading.
		_ = f.Close()
	}()

	return readByChunks(f, request.ReadLimit, server, c)
}

func readByChunks(f io.Reader, readLimit int64, server bspb.ByteStream_ReadServer, comp repb.Compressor_Value) error {
	// Prepare a buffer to read the file into.
	bufSize := maxChunkSize
	if readLimit > 0 && readLimit < bufSize {
		bufSize = readLimit
	}
	buf := bytes.NewBuffer(make([]byte, bufSize))
	var dataReader io.Reader
	switch comp {
	case repb.Compressor_IDENTITY:
		// Read the data from the file directly.
		dataReader = f
	case repb.Compressor_ZSTD:
		// Read the data via the pipe from Encoder.
		ir, iw := io.Pipe()
		defer ir.Close()
		enc, err := zstd.NewWriter(iw)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		go func() {
			_, err := io.Copy(enc, f)
			if e := enc.Close(); e != nil && err == nil {
				err = e
			}
			iw.CloseWithError(err)
		}()
		dataReader = ir
	default:
		return status.Errorf(codes.InvalidArgument, "unsupported compressor: %q", comp)
	}

	// Send the requested data to the client in chunks.
	for {
		buf.Reset()
		n, err := io.CopyN(buf, dataReader, bufSize)
		if n > 0 {
			if err := server.Send(&bspb.ReadResponse{
				Data: buf.Bytes(),
			}); err != nil {
				return status.Errorf(codes.Internal, "failed to send data to client: %v", err)
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return status.Errorf(codes.Internal, "failed to read data from file: %v", err)
		}
	}
	return nil
}

// Write implements the ByteStream.Write RPC.
func (s *Service) Write(server bspb.ByteStream_WriteServer) error {
	resourceName, err := s.write(server)
	if err != nil {
		log.Printf("🚨 Write(%v) => Error: %v", resourceName, err)
	} else {
		log.Printf("✅ Write(%v) => OK", resourceName)
	}
	return err
}

type committedSizeCounter struct {
	Size int64
}

func (c *committedSizeCounter) Write(b []byte) (int, error) {
	n := len(b)
	c.Size += int64(n)
	return n, nil
}

func (s *Service) write(server bspb.ByteStream_WriteServer) (resource string, err error) {
	expectedDigest := digest.Empty
	ourHash := sha256.New()
	committedSize := &committedSizeCounter{}
	finishedWriting := false
	var tempFile *os.File
	var tempPath string
	var comp repb.Compressor_Value
	var dataDest io.Writer
	// variables for compression.
	var ir *io.PipeReader
	var iw *io.PipeWriter
	var decCh chan error
	defer func() {
		if tempFile != nil {
			if err := tempFile.Close(); err != nil {
				log.Printf("could not close temporary file %q: %v", tempPath, err)
			}
			if err := os.Remove(tempPath); err != nil {
				log.Printf("could not delete temporary file %q: %v", tempPath, err)
			}
		}
		if iw != nil {
			iw.Close()
		}
		if ir != nil {
			ir.Close()
		}
	}()

	for {
		// Receive a request from the client.
		request, err := server.Recv()
		if errors.Is(err, io.EOF) {
			// If the client closed the connection without ever sending a request, return an error.
			if resource == "" {
				return resource, status.Error(codes.InvalidArgument, "no resource name provided")
			}

			// Check that the client set "finish_write" to true.
			if !finishedWriting {
				return resource, status.Error(codes.InvalidArgument, "upload finished without finish_write set")
			}

			// Check that the digests (= hash and size) match.
			d := digest.Digest{Hash: hex.EncodeToString(ourHash.Sum(nil)), Size: committedSize.Size}
			if d != expectedDigest {
				return resource, status.Errorf(codes.InvalidArgument, "computed digest %v did not match expected digest %v", d, expectedDigest)
			}

			// Move the temporary file to the CAS.
			if err := s.cas.Adopt(expectedDigest, tempPath); err != nil {
				return resource, status.Errorf(codes.Internal, "failed to move file into CAS: %v", err)
			}

			// Send the response to the client.
			if err := server.SendAndClose(&bspb.WriteResponse{
				CommittedSize: committedSize.Size,
			}); err != nil {
				return resource, status.Errorf(codes.Internal, "failed to send response to client: %v", err)
			}

			// Yay, we're done!
			return resource, nil
		} else if err != nil {
			return resource, status.Errorf(codes.Internal, "failed to receive request from client: %v", err)
		}

		// If the resource name is empty, this is the first request from the client.
		if resource == "" {
			if request.ResourceName == "" {
				return resource, status.Error(codes.InvalidArgument, "must set resource name on first request")
			}
			resource = request.ResourceName
			var u uuid.UUID
			expectedDigest, u, comp, err = parseWriteResource(request.ResourceName)
			if err != nil {
				return resource, err
			}
			tempPath = filepath.Join(s.uploadDir, u.String())
		} else {
			// Ensure that the resource name is either not set, or the same as the first request.
			if request.ResourceName != "" && request.ResourceName != resource {
				return resource, status.Errorf(codes.InvalidArgument, "resource name changed (%v => %v)", resource, request.ResourceName)
			}
		}

		if finishedWriting {
			return resource, status.Error(codes.InvalidArgument, "cannot write more data after finish_write was true")
		}

		// If the resource was uploaded concurrently and already exists in our CAS, immediately return success.
		if s.cas.Has(expectedDigest) {
			return resource, server.SendAndClose(&bspb.WriteResponse{
				CommittedSize: expectedDigest.Size,
			})
		}

		// Create the file for the pending upload if this is the first write.
		if tempFile == nil && !finishedWriting {
			tempFile, err = os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if err != nil {
				if errors.Is(err, fs.ErrExist) {
					return resource, status.Error(codes.InvalidArgument, "upload with same uuid already in progress")
				}
				return resource, status.Errorf(codes.Internal, "could not create temporary file for upload: %v", err)
			}
			finalDest := io.MultiWriter(tempFile, ourHash, committedSize)
			switch comp {
			case repb.Compressor_IDENTITY:
				// Consume the received data as is.
				dataDest = finalDest
			case repb.Compressor_ZSTD:
				// Consume the received data via decoder.
				ir, iw = io.Pipe()
				dec, err := zstd.NewReader(ir)
				if err != nil {
					return resource, status.Errorf(codes.Internal, "could not create decoder: %v", err)
				}
				defer dec.Close()
				decCh = make(chan error, 1)
				go func() {
					_, derr := io.Copy(finalDest, dec)
					decCh <- derr
				}()
				dataDest = iw // Pipe the stream data to the decoder.
			default:
				return resource, status.Errorf(codes.InvalidArgument, "unsupported compressor: %q", comp)
			}
		}

		// Append the received data to the destination.
		_, err = dataDest.Write(request.Data)
		if err != nil {
			return resource, status.Errorf(codes.Internal, "failed to write data: %v", err)
		}

		// If the file is already larger than the expected size, something is wrong - return an error.
		if committedSize.Size > expectedDigest.Size {
			return resource, status.Errorf(codes.InvalidArgument, "received %d bytes, more than expected %d", committedSize.Size, expectedDigest.Size)
		}

		if request.FinishWrite {
			finishedWriting = true
			if comp != repb.Compressor_IDENTITY {
				iw.Close() // Close the pipe to the decoder, so that the decoder can receive io.EOF.
				iw = nil
				err = <-decCh
				if err != nil {
					return resource, status.Errorf(codes.Internal, "failed to finish decoder: %v", err)
				}
			}
			err = tempFile.Close()
			if err != nil {
				return resource, status.Errorf(codes.Internal, "could not close temporary file: %v", err)
			}
			// We set tempFile to `nil` *after* checking for an error to give the defer handler a last
			// chance to clean things up...
			tempFile = nil
		}
	}
}

// QueryWriteStatus implements the ByteStream.QueryWriteStatus RPC.
func (s *Service) QueryWriteStatus(ctx context.Context, request *bspb.QueryWriteStatusRequest) (*bspb.QueryWriteStatusResponse, error) {
	response, err := s.queryWriteStatus(request)
	if err != nil {
		log.Printf("🚨 QueryWriteStatus(%v) failed: %s", request.ResourceName, err)
	} else {
		log.Printf("✅ QueryWriteStatus(%v) succeeded", request.ResourceName)
	}
	return response, err
}

func (s *Service) queryWriteStatus(request *bspb.QueryWriteStatusRequest) (*bspb.QueryWriteStatusResponse, error) {
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
func (s *Service) FindMissingBlobs(ctx context.Context, request *repb.FindMissingBlobsRequest) (*repb.FindMissingBlobsResponse, error) {
	response, err := s.findMissingBlobs(request)
	if err != nil {
		log.Printf("🚨 FindMissingBlobs(%d blobs) => Error: %v", len(request.BlobDigests), err)
	} else {
		log.Printf("✅ FindMissingBlobs(%d blobs) => OK (%d missing)", len(request.BlobDigests), len(response.MissingBlobDigests))
	}
	return response, err
}

func (s *Service) findMissingBlobs(request *repb.FindMissingBlobsRequest) (*repb.FindMissingBlobsResponse, error) {
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
func (s *Service) BatchUpdateBlobs(ctx context.Context, request *repb.BatchUpdateBlobsRequest) (*repb.BatchUpdateBlobsResponse, error) {
	response, err := s.batchUploadBlobs(request)
	if err != nil {
		log.Printf("🚨 BatchUpdateBlobs(%v blobs) => Error: %v", len(request.Requests), err)
	} else {
		log.Printf("✅ BatchUpdateBlobs(%v blobs) => OK", len(request.Requests))
	}
	return response, err
}

func (s *Service) batchUploadBlobs(request *repb.BatchUpdateBlobsRequest) (*repb.BatchUpdateBlobsResponse, error) {
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

func (s *Service) BatchReadBlobs(ctx context.Context, request *repb.BatchReadBlobsRequest) (*repb.BatchReadBlobsResponse, error) {
	response, err := s.batchReadBlobs(request)
	if err != nil {
		log.Printf("🚨 BatchReadBlobs(%v blobs) => Error: %v", len(request.Digests), err)
	} else {
		log.Printf("✅ BatchReadBlobs(%v blobs) => OK", len(request.Digests))
	}
	return response, err
}

func (s *Service) batchReadBlobs(request *repb.BatchReadBlobsRequest) (*repb.BatchReadBlobsResponse, error) {
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

		// Read the blob from the CAS.
		data, err := s.cas.Get(dg)
		if err != nil {
			var mbe *MissingBlobsError
			if !errors.As(err, &mbe) {
				// The blob doesn't exist. Add a response with an appropriate status code.
				response.Responses = append(response.Responses, &repb.BatchReadBlobsResponse_Response{
					Digest: d,
					Status: status.New(codes.NotFound, "").Proto(),
				})
				continue
			}
			return nil, status.Errorf(codes.Internal, "failed to read blob: %v", err)
		}

		// The blob exists. Add a response with the data.
		response.Responses = append(response.Responses, &repb.BatchReadBlobsResponse_Response{
			Digest: d,
			Data:   data,
			Status: status.New(codes.OK, "").Proto(),
		})
	}

	// Return the response to the client.
	return response, nil
}

func (s *Service) GetTree(request *repb.GetTreeRequest, treeServer repb.ContentAddressableStorage_GetTreeServer) error {
	if err := s.getTree(request, treeServer); err != nil {
		log.Printf("🚨 GetTree(%v) => Error: %v", request.RootDigest, err)
		return err
	} else {
		log.Printf("✅ GetTree(%v) => OK", request.RootDigest)
	}
	return nil
}

func (s *Service) getTree(request *repb.GetTreeRequest, treeServer repb.ContentAddressableStorage_GetTreeServer) error {
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
	dirs, err := s.cas.FlattenDirectory(d)
	if err != nil {
		return err
	}

	// Prepare a response that we can fill in.
	response := &repb.GetTreeResponse{
		Directories: dirs,
	}

	// Send the tree to the client.
	return treeServer.Send(response)
}
