// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package execution implements the REAPI Execution service.
package execution

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"time"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
	errpb "google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"infra/build/kajiya/actioncache"
	"infra/build/kajiya/blobstore"
)

// Service implements the REAPI Execution service.
type Service struct {
	repb.UnimplementedExecutionServer

	executor    ExecutorInterface
	actionCache *actioncache.ActionCache
	cas         *blobstore.ContentAddressableStorage

	// actionDigestDeduper merges multiple parallel requests for the same action.
	actionDigestDeduper singleflight.Group
}

// ExecutorInterface is an interface of Executor.
type ExecutorInterface interface {
	Execute(*repb.Action) (*repb.ActionResult, error)
}

// Register creates and registers a new Service with the given gRPC server.
func Register(s *grpc.Server, executor ExecutorInterface, ac *actioncache.ActionCache, cas *blobstore.ContentAddressableStorage) error {
	service, err := NewService(executor, ac, cas)
	if err != nil {
		return err
	}
	repb.RegisterExecutionServer(s, service)
	return nil
}

// NewService creates a new Service.
func NewService(executor ExecutorInterface, ac *actioncache.ActionCache, cas *blobstore.ContentAddressableStorage) (*Service, error) {
	if executor == nil {
		return nil, fmt.Errorf("executor must be set")
	}

	if cas == nil {
		return nil, fmt.Errorf("cas must be set")
	}

	return &Service{
		executor:    executor,
		actionCache: ac,
		cas:         cas,
	}, nil
}

// Execute executes the given action and returns the result.
func (s *Service) Execute(request *repb.ExecuteRequest, executeServer repb.Execution_ExecuteServer) error {
	// Just for fun, measure how long the execution takes and log it.
	start := time.Now()
	err := s.execute(request, executeServer)
	duration := time.Since(start)

	if err != nil {
		log.Printf("🚨 Execute(%v) => Error: %v", request.ActionDigest, err)

		var mberr *blobstore.MissingBlobsError
		if errors.As(err, &mberr) {
			return formatMissingBlobsError(mberr)
		} else if _, ok := status.FromError(err); !ok {
			// Any error that reaches this point and is not already a gRPC status is an
			// unexpected internal error and not due to client input. We wrap it in a
			// status error with the Internal code to ensure we signal this condition
			// correctly to the client.
			return status.Errorf(codes.Internal, "failed to execute action: %v", err)
		}
		return err
	}

	log.Printf("🎉 Execute(%v) => OK (%v)", request.ActionDigest, duration)
	return nil
}

func (s *Service) execute(request *repb.ExecuteRequest, executeServer repb.Execution_ExecuteServer) error {
	// If the client explicitly specifies a DigestFunction, ensure that it's SHA256.
	if request.DigestFunction != repb.DigestFunction_UNKNOWN && request.DigestFunction != repb.DigestFunction_SHA256 {
		return status.Errorf(codes.InvalidArgument, "hash function %q is not supported", request.DigestFunction.String())
	}

	// Parse the action digest.
	actionDigest, err := digest.NewFromProto(request.ActionDigest)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid action digest: %v", err)
	}

	// Fetch the Action from the CAS.
	action := &repb.Action{}
	if err := s.cas.Proto(actionDigest, action); err != nil {
		return err
	}

	// If we're not supposed to cache the result, just execute the action and return the result.
	if action.DoNotCache {
		ar, err := s.executor.Execute(action)
		if err != nil {
			return err
		}
		reply, err := wrapActionResult(actionDigest, ar, false)
		if err != nil {
			return err
		}
		return executeServer.Send(reply)
	}

	// According to the REAPI specification, in-flight requests for the same `Action` may be
	// merged unless the `DoNotCache` bit is set. This improves efficiency and performance by
	// avoiding duplicate work.
	ar, err, _ := s.actionDigestDeduper.Do(actionDigest.String(), func() (interface{}, error) {
		// If we have an action cache, check if the action is already cached.
		if s.actionCache != nil && !request.SkipCacheLookup {
			ar, err := s.actionCache.Get(actionDigest)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("failed to get action from cache: %w", err)
			}
			if ar != nil {
				return wrapActionResult(actionDigest, ar, true)
			}
		}

		// Cache miss, so we have to execute the action.
		ar, err := s.executor.Execute(action)
		if err != nil {
			return nil, err
		}

		// Store the result in the action cache if possible. We only cache successful
		// result, as it's always possible that a failed action is due to a transient
		// issue that will be resolved on the next execution.
		if s.actionCache != nil && ar.ExitCode == 0 {
			if err = s.actionCache.Put(actionDigest, ar); err != nil {
				return nil, fmt.Errorf("failed to put action into cache: %w", err)
			}
		}

		return wrapActionResult(actionDigest, ar, false)
	})
	if err != nil {
		return err
	}
	if err = executeServer.Send(ar.(*longrunningpb.Operation)); err != nil {
		return fmt.Errorf("failed to send result to client: %w", err)
	}

	return nil
}

// Return the list of missing blobs as a "FailedPrecondition" error as
// described in the Remote Execution API.
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

func wrapActionResult(d digest.Digest, r *repb.ActionResult, cached bool) (*longrunningpb.Operation, error) {
	// Construct some metadata for the execution operation and wrap it in an Any.
	md, err := anypb.New(&repb.ExecuteOperationMetadata{
		Stage:        repb.ExecutionStage_COMPLETED,
		ActionDigest: d.ToProto(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Put the action result into an Any-wrapped ExecuteResponse.
	resp, err := anypb.New(&repb.ExecuteResponse{
		Result:       r,
		CachedResult: cached,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal response: %w", err)
	}

	// Generate a unique operation name.
	// TODO: Use a real operation ID that's consistent across the lifetime of the operation.
	opName, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("failed to generate operation ID: %w", err)
	}

	// Wrap all the protos in another proto and return it.
	op := &longrunningpb.Operation{
		Name:     fmt.Sprintf("operations/%s", opName),
		Metadata: md,
		Done:     true,
		Result: &longrunningpb.Operation_Response{
			Response: resp,
		},
	}
	return op, nil
}

// WaitExecution waits for the specified execution to complete.
func (s *Service) WaitExecution(request *repb.WaitExecutionRequest, executionServer repb.Execution_WaitExecutionServer) error {
	return status.Error(codes.Unimplemented, "WaitExecution is not implemented")
}
