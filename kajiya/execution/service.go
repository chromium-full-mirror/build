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
	"sync"
	"time"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/google/uuid"
	errpb "google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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

	// actionDigestMutexes is a map of action digests to mutexes.
	// It's used to prevent multiple clients from executing the same action at the same time.
	// TODO: Implement garbage collection of old entries?
	actionDigestMutexes sync.Map
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
	} else {
		log.Printf("🎉 Execute(%v) => OK (%v)", request.ActionDigest, duration)
	}
	return err
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

	// If we have an action cache, check if the action is already cached.
	// If so, return the cached result.
	if s.actionCache != nil && !request.SkipCacheLookup {
		resp, err := s.checkActionCache(actionDigest)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return status.Errorf(codes.Internal, "failed to check action cache: %v", err)
			}
		}
		if resp != nil {
			return executeServer.Send(resp)
		}
	}

	// Fetch the Action from the CAS.
	action, err := s.getAction(actionDigest)
	if err != nil {
		return err
	}

	// According to the REAPI specification, in-flight requests for the same `Action` may not be
	// merged if the `DoNotCache` bit is set.
	if !action.DoNotCache {
		// By locking on the action digest, we ensure that only one client can execute the action at
		// a time. Other clients will block until the action is done executing, and then they will
		// get the result from the cache. This improves efficiency and performance by avoiding
		// duplicate work.
		actionDigestMutex, _ := s.actionDigestMutexes.LoadOrStore(actionDigest, &sync.Mutex{})
		actionDigestMutex.(*sync.Mutex).Lock()
		defer actionDigestMutex.(*sync.Mutex).Unlock()
	}

	// Execute the action.
	actionResult, err := s.executor.Execute(action)
	if err != nil {
		var mbe *blobstore.MissingBlobsError
		if errors.As(err, &mbe) {
			return s.formatMissingBlobsError(*mbe)
		}
		// Otherwise, return a generic internal error.
		return status.Errorf(codes.Internal, "failed to execute action: %v", err)
	}

	// Store the result in the action cache.
	if s.actionCache != nil && !action.DoNotCache && actionResult.ExitCode == 0 {
		if err = s.actionCache.Put(actionDigest, actionResult); err != nil {
			return status.Errorf(codes.Internal, "failed to put action into cache: %v", err)
		}
	}

	// Send the result to the client.
	op, err := s.wrapActionResult(actionDigest, actionResult, false)
	if err != nil {
		return err
	}
	if err = executeServer.Send(op); err != nil {
		return status.Errorf(codes.Internal, "failed to send result to client: %v", err)
	}

	return nil
}

func (s *Service) checkActionCache(d digest.Digest) (*longrunningpb.Operation, error) {
	// Try to get the result from the cache.
	actionResult, err := s.actionCache.Get(d)
	if err != nil {
		return nil, err
	}

	// Nice, cache hit! Let's wrap it up and send it to the client.
	op, err := s.wrapActionResult(d, actionResult, true)
	if err != nil {
		return nil, err
	}
	return op, nil
}

// Return the list of missing blobs as a "FailedPrecondition" error as
// described in the Remote Execution API.
func (s *Service) formatMissingBlobsError(e blobstore.MissingBlobsError) error {
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

func (s *Service) wrapActionResult(d digest.Digest, r *repb.ActionResult, cached bool) (*longrunningpb.Operation, error) {
	// Construct some metadata for the execution operation and wrap it in an Any.
	md, err := anypb.New(&repb.ExecuteOperationMetadata{
		Stage:        repb.ExecutionStage_COMPLETED,
		ActionDigest: d.ToProto(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to marshal metadata: %v", err)
	}

	// Put the action result into an Any-wrapped ExecuteResponse.
	resp, err := anypb.New(&repb.ExecuteResponse{
		Result:       r,
		CachedResult: cached,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to marshal response: %v", err)
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

// getAction fetches the repb.Action with the given digest.Digest from our CAS.
func (s *Service) getAction(d digest.Digest) (*repb.Action, error) {
	// Fetch the Action from the CAS.
	actionBytes, err := s.cas.Get(d)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get action from CAS: %v", err)
	}

	// Unmarshal the Action.
	action := &repb.Action{}
	err = proto.Unmarshal(actionBytes, action)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmarshal action: %v", err)
	}

	return action, nil
}

// WaitExecution waits for the specified execution to complete.
func (s *Service) WaitExecution(request *repb.WaitExecutionRequest, executionServer repb.Execution_WaitExecutionServer) error {
	return status.Error(codes.Unimplemented, "WaitExecution is not implemented")
}
