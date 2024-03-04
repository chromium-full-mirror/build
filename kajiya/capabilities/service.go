// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package capabilities implements the REAPI Capabilities service.
package capabilities

import (
	"context"
	"log"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	semverpb "github.com/bazelbuild/remote-apis/build/bazel/semver"
	"google.golang.org/grpc"
)

// Service implements the REAPI Capabilities service.
type Service struct {
	repb.UnimplementedCapabilitiesServer
}

// Register creates and registers a new Service with the given gRPC server.
func Register(s *grpc.Server) {
	repb.RegisterCapabilitiesServer(s, NewService())
}

// NewService creates a new Service.
func NewService() *Service {
	return &Service{}
}

// GetCapabilities returns the capabilities of the server.
func (s *Service) GetCapabilities(ctx context.Context, request *repb.GetCapabilitiesRequest) (*repb.ServerCapabilities, error) {
	response, err := s.getCapabilities(request)
	if err != nil {
		log.Printf("⚠️ GetCapabilities(%v) => Error: %v", request, err)
	} else {
		log.Printf("✅ GetCapabilities(%v) => OK", request)
	}
	return response, err
}

func (s *Service) getCapabilities(request *repb.GetCapabilitiesRequest) (*repb.ServerCapabilities, error) {
	// Return the capabilities.
	return &repb.ServerCapabilities{
		CacheCapabilities: &repb.CacheCapabilities{
			DigestFunctions: []repb.DigestFunction_Value{
				repb.DigestFunction_SHA256,
			},
			ActionCacheUpdateCapabilities: &repb.ActionCacheUpdateCapabilities{
				UpdateEnabled: true,
			},
			CachePriorityCapabilities: &repb.PriorityCapabilities{
				Priorities: []*repb.PriorityCapabilities_PriorityRange{
					{
						MinPriority: 0,
						MaxPriority: 0,
					},
				},
			},
			MaxBatchTotalSizeBytes:      0,                                           // no limit.
			SymlinkAbsolutePathStrategy: repb.SymlinkAbsolutePathStrategy_DISALLOWED, // Same as RBE.
		},
		ExecutionCapabilities: &repb.ExecutionCapabilities{
			DigestFunction: repb.DigestFunction_SHA256,
			DigestFunctions: []repb.DigestFunction_Value{
				repb.DigestFunction_SHA256,
			},
			ExecEnabled: true,
			ExecutionPriorityCapabilities: &repb.PriorityCapabilities{
				Priorities: []*repb.PriorityCapabilities_PriorityRange{
					{
						MinPriority: 0,
						MaxPriority: 0,
					},
				},
			},
		},
		LowApiVersion:  &semverpb.SemVer{Major: 2, Minor: 0},
		HighApiVersion: &semverpb.SemVer{Major: 2, Minor: 0}, // RBE does not support higher versions, so we don't either.
	}, nil
}
