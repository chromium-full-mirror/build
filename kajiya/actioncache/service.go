// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package actioncache implements the REAPI ActionCache service.
package actioncache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/server"
)

// Service implements the REAPI ActionCache service.
type Service struct {
	repb.UnimplementedActionCacheServer

	// The ActionCache to use for storing ActionResults.
	ac *ActionCache

	// The blobstore.ContentAddressableStorage to use for reading blobs.
	cas *blobstore.ContentAddressableStorage

	config server.Config
}

// Register creates and registers a new Service with the given gRPC server.
func Register(s *grpc.Server, ac *ActionCache, cas *blobstore.ContentAddressableStorage, cfg server.Config) error {
	if ac == nil {
		return fmt.Errorf("ac must be set")
	}

	if cas == nil {
		return fmt.Errorf("cas must be set")
	}

	service := &Service{
		ac:     ac,
		cas:    cas,
		config: cfg,
	}
	repb.RegisterActionCacheServer(s, service)
	return nil
}

// GetActionResult returns the ActionResult for a given action digest.
func (s *Service) GetActionResult(ctx context.Context, request *repb.GetActionResultRequest) (resp *repb.ActionResult, err error) {
	defer func() {
		if err != nil {
			if status.Code(err) == codes.NotFound {
				slog.Warn("GetActionResult", "action", request.ActionDigest, "result", "cache miss")
			} else {
				slog.Error("GetActionResult", "action", request.ActionDigest, "error", err)
			}
		} else {
			slog.Info("GetActionResult", "action", request.ActionDigest, "result", "cache hit")
		}
	}()

	fn, actionDigest, err := s.config.ResolveDigest(request.DigestFunction, request.ActionDigest)
	if err != nil {
		return nil, err
	}

	actionResult, err := s.ac.Get(fn, actionDigest)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, status.Errorf(codes.NotFound, "action digest %s not found in cache", actionDigest)
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	return actionResult, nil
}

// UpdateActionResult stores an ActionResult for a given action digest on disk.
func (s *Service) UpdateActionResult(ctx context.Context, request *repb.UpdateActionResultRequest) (resp *repb.ActionResult, err error) {
	defer func() {
		if err != nil {
			slog.Error("UpdateActionResult", "action", request.ActionDigest, "error", err)
		} else {
			slog.Info("UpdateActionResult", "action", request.ActionDigest)
		}
	}()

	// Check that the client didn't send inline stdout / stderr data.
	if request.ActionResult.StdoutRaw != nil {
		return nil, status.Error(codes.InvalidArgument, "client should not populate stdout_raw during upload")
	}
	if request.ActionResult.StderrRaw != nil {
		return nil, status.Error(codes.InvalidArgument, "client should not populate stderr_raw during upload")
	}

	// Check that the action digest is valid.
	fn, actionDigest, err := s.config.ResolveDigest(request.DigestFunction, request.ActionDigest)
	if err != nil {
		return nil, err
	}

	// Check that the action is present in our CAS.
	if !s.cas.Has(fn, actionDigest) {
		return nil, status.Errorf(codes.NotFound, "action digest %s not found in CAS", actionDigest)
	}

	// If the action result contains a stdout digest, check that it is present in our CAS.
	if request.ActionResult.StdoutDigest != nil {
		stdoutDigest, err := fn.FromProto(request.ActionResult.StdoutDigest)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		if !s.cas.Has(fn, stdoutDigest) {
			return nil, status.Errorf(codes.NotFound, "stdout digest %s not found in CAS", stdoutDigest)
		}
	}

	// Same for stderr.
	if request.ActionResult.StderrDigest != nil {
		stderrDigest, err := fn.FromProto(request.ActionResult.StderrDigest)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		if !s.cas.Has(fn, stderrDigest) {
			return nil, status.Errorf(codes.NotFound, "stderr digest %s not found in CAS", stderrDigest)
		}
	}

	// TODO: Check that all the output files are present in our CAS.

	// Store the action result.
	if err := s.ac.Put(fn, actionDigest, request.ActionResult); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Return the action result.
	return request.ActionResult, nil
}
