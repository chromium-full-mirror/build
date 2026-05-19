// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package execution implements the REAPI Execution service.
package execution

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"runtime"
	"time"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/google/uuid"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
	errpb "google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"go.chromium.org/build/kajiya/actioncache"
	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/digest"
	"go.chromium.org/build/kajiya/execution/model"
)

// Service implements the REAPI Execution service.
type Service struct {
	repb.UnimplementedExecutionServer

	executor    ExecutorInterface
	actionCache *actioncache.ActionCache
	cas         *blobstore.ContentAddressableStorage
	sem         *semaphore.Weighted

	// actionDigestDeduper merges multiple parallel requests for the same action.
	actionDigestDeduper singleflight.Group
}

// ExecutorInterface is an interface of Executor.
type ExecutorInterface interface {
	Execute(*model.Action) (*repb.ActionResult, error)
}

// Register creates and registers a new Service with the given gRPC server.
func Register(s *grpc.Server, executor ExecutorInterface, ac *actioncache.ActionCache, cas *blobstore.ContentAddressableStorage) error {
	if executor == nil {
		return fmt.Errorf("executor must be set")
	}

	if cas == nil {
		return fmt.Errorf("cas must be set")
	}

	service := &Service{
		executor:    executor,
		actionCache: ac,
		cas:         cas,
		sem:         semaphore.NewWeighted(int64(runtime.GOMAXPROCS(0))),
	}

	repb.RegisterExecutionServer(s, service)
	return nil
}

func Metadata(ctx context.Context) (*repb.RequestMetadata, error) {
	// Extract the "build.bazel.remote.execution.v2.requestmetadata-bin" metadata
	// and convert it to a string slice for logging.
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		// Metadata is optional, so it's not an error if the request doesn't have any.
		return nil, nil
	}
	bmdStrs, ok := md["build.bazel.remote.execution.v2.requestmetadata-bin"]
	if !ok {
		return nil, nil
	}
	if len(bmdStrs) != 1 {
		return nil, fmt.Errorf("expected exactly one 'build.bazel.remote.execution.v2.requestmetadata-bin' metadata entry, got %d", len(bmdStrs))
	}

	// Unmarshal the metadata from the binary string.
	bmd := &repb.RequestMetadata{}
	if err := proto.Unmarshal([]byte(bmdStrs[0]), bmd); err != nil {
		return nil, fmt.Errorf("failed to unmarshal request metadata: %w", err)
	}

	// Return the unmarshaled metadata.
	return bmd, nil
}

// Execute executes the given action and returns the result.
func (s *Service) Execute(request *repb.ExecuteRequest, executeServer repb.Execution_ExecuteServer) (err error) {
	// Just for fun, measure how long the execution takes and log it.
	start := time.Now()

	// Error post-processing & logging.
	defer func() {
		duration := time.Since(start)
		if err != nil {
			var mberr *blobstore.MissingBlobsError
			var iaerr *model.InvalidActionError
			switch {
			case errors.As(err, &mberr):
				err = formatMissingBlobsError(mberr)
			case errors.As(err, &iaerr):
				// Client-supplied Action/Command was malformed or unsupported on
				// this server. Use InvalidArgument so clients do not retry.
				err = status.Error(codes.InvalidArgument, iaerr.Error())
			default:
				if _, ok := status.FromError(err); !ok {
					// Any error that reaches this point and is not already a gRPC status is an
					// unexpected internal error and not due to client input. We wrap it in a
					// status error with the Internal code to ensure we signal this condition
					// correctly to the client.
					err = status.Errorf(codes.Internal, "failed to execute action: %v", err)
				}
			}
			slog.Error("Execute", "action", request.ActionDigest, "duration", duration, "error", err)
		} else {
			slog.Info("Execute", "action", request.ActionDigest, "duration", duration)
		}
	}()

	// TODO: use the metadata for something useful, for now we're just validating it.
	_, err = Metadata(executeServer.Context())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "request contained invalid metadata: %v", err)
	}

	// If the client explicitly specifies a DigestFunction, ensure that it's SHA256.
	if request.DigestFunction != repb.DigestFunction_UNKNOWN && request.DigestFunction != repb.DigestFunction_SHA256 {
		return status.Errorf(codes.InvalidArgument, "hash function %q is not supported", request.DigestFunction.String())
	}

	// Generate a unique identifier for this operation.
	opName := uuidgen.NewV7()

	actionDigest, err := digest.NewFromProto(request.ActionDigest)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid action digest: %v", err)
	}

	// If we have an action cache, check if the action is already cached.
	if s.actionCache != nil && !request.SkipCacheLookup {
		// Tell the client that we're in CACHE_CHECK stage now.
		reply, err := executionStage(request.ActionDigest, opName, repb.ExecutionStage_CACHE_CHECK)
		if err != nil {
			return err
		}
		if err = executeServer.Send(reply); err != nil {
			return err
		}

		// Check the action cache and if we get a hit, send the result back.
		ar, err := s.actionCache.Get(actionDigest)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("failed to get action from cache: %w", err)
		}
		if ar != nil {
			reply, err := executionComplete(request.ActionDigest, opName, ar, true)
			if err != nil {
				return err
			}
			return executeServer.Send(reply)
		}
	}

	// Cache miss, so we have to load & parse the action proto, then execute the action.
	action, err := model.LoadAction(actionDigest, s.cas)
	if err != nil {
		return err
	}

	// Tell the client that we're in QUEUED stage now.
	reply, err := executionStage(request.ActionDigest, opName, repb.ExecutionStage_QUEUED)
	if err != nil {
		return err
	}
	if err = executeServer.Send(reply); err != nil {
		return err
	}

	// According to the REAPI specification, in-flight requests for the same `Action` may be
	// merged unless the `DoNotCache` bit is set. This improves efficiency and performance by
	// avoiding duplicate work.
	dedupKey := actionDigest.Hash
	if action.DoNotCache {
		dedupKey = opName.String()
	}
	ar, err, _ := s.actionDigestDeduper.Do(dedupKey, func() (any, error) {
		// Acquire a semaphore to limit the number of concurrent executions.
		err = s.sem.Acquire(executeServer.Context(), 1)
		if err != nil {
			return nil, err
		}
		defer s.sem.Release(1)

		// Tell the client that we're in EXECUTING stage now.
		reply, err := executionStage(request.ActionDigest, opName, repb.ExecutionStage_EXECUTING)
		if err != nil {
			return nil, err
		}
		if err = executeServer.Send(reply); err != nil {
			return nil, err
		}

		// Execute the action.
		ar, err := s.executor.Execute(action)
		if err != nil {
			return nil, err
		}

		// Store the result in the action cache if possible. We only cache successful
		// results, as it's always possible that a failed action is due to a transient
		// issue that will be resolved on the next execution.
		if !action.DoNotCache && s.actionCache != nil && ar.ExitCode == 0 {
			if err = s.actionCache.Put(action.ActionDigest, ar); err != nil {
				slog.Error("failed to put action into cache", "error", err)
			}
		}

		return ar, nil
	})
	if err != nil {
		return err
	}

	reply, err = executionComplete(request.ActionDigest, opName, ar.(*repb.ActionResult), false)
	if err != nil {
		return err
	}
	if err = executeServer.Send(reply); err != nil {
		return fmt.Errorf("failed to send result to client: %w", err)
	}
	return nil
}

// Return the list of missing blobs as a "FailedPrecondition" error as described in the Remote
// Execution API.
func formatMissingBlobsError(e *blobstore.MissingBlobsError) error {
	violations := make([]*errpb.PreconditionFailure_Violation, 0, len(e.Blobs))
	for _, b := range e.Blobs {
		violations = append(violations, &errpb.PreconditionFailure_Violation{
			Type:    "MISSING",
			Subject: fmt.Sprintf("blobs/%s/%d", b.Hash, b.Size),
		})
	}

	st, err := status.New(codes.FailedPrecondition, "missing blobs").WithDetails(&errpb.PreconditionFailure{
		Violations: violations,
	})
	if err != nil {
		return status.Errorf(codes.Internal, "failed to create status: %v", err)
	}
	return st.Err()
}

// executionStage returns an Operation message with an update on the current state of opName
func executionStage(actionDigest *repb.Digest, opName uuid.UUID, stage repb.ExecutionStage_Value) (*longrunningpb.Operation, error) {
	md, err := anypb.New(&repb.ExecuteOperationMetadata{
		ActionDigest: actionDigest,
		Stage:        stage,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}
	return &longrunningpb.Operation{
		Name:     fmt.Sprintf("operations/%s", opName),
		Metadata: md,
		Done:     false,
	}, nil
}

// executionComplete returns an Operation message with the final ExecuteResponse for an action
func executionComplete(actionDigest *repb.Digest, opName uuid.UUID, r *repb.ActionResult, cached bool) (*longrunningpb.Operation, error) {
	op, err := executionStage(actionDigest, opName, repb.ExecutionStage_COMPLETED)
	if err != nil {
		return nil, err
	}

	// Put the action result into an Any-wrapped ExecuteResponse.
	resp, err := anypb.New(&repb.ExecuteResponse{
		Result:       r,
		CachedResult: cached,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal response: %w", err)
	}

	// Wrap the proto in another proto and return it.
	op.Done = true
	op.Result = &longrunningpb.Operation_Response{
		Response: resp,
	}
	return op, nil
}

// WaitExecution waits for the specified execution to complete.
func (s *Service) WaitExecution(request *repb.WaitExecutionRequest, executionServer repb.Execution_WaitExecutionServer) error {
	return status.Error(codes.Unimplemented, "WaitExecution is not implemented")
}
