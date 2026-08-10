// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package capabilities implements the REAPI Capabilities service.
package capabilities

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
	semverpb "go.chromium.org/build/remote-apis/build/bazel/semver"

	"go.chromium.org/build/kajiya/server"
)

// Service implements the REAPI Capabilities service.
type Service struct {
	repb.UnimplementedCapabilitiesServer

	config server.Config
}

// Register creates and registers a new Service with the given gRPC server.
func Register(s *grpc.Server, cfg server.Config) {
	repb.RegisterCapabilitiesServer(s, &Service{
		config: cfg,
	})
}

// GetCapabilities returns the capabilities of the server.
func (s *Service) GetCapabilities(ctx context.Context, request *repb.GetCapabilitiesRequest) (resp *repb.ServerCapabilities, err error) {
	defer func() {
		if err != nil {
			slog.Error("GetCapabilities", "request", request, "error", err)
		} else {
			slog.Info("GetCapabilities", "request", request)
		}
	}()

	advertised := s.config.AdvertisedDigestFunctions()
	values := make([]repb.DigestFunction_Value, len(advertised))
	for i, fn := range advertised {
		values[i] = fn.Value()
	}

	// Return the capabilities.
	return &repb.ServerCapabilities{
		CacheCapabilities: &repb.CacheCapabilities{
			DigestFunctions: values,
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
			MaxBatchTotalSizeBytes:      s.config.MaxBatchTotalSizeBytes,
			SymlinkAbsolutePathStrategy: repb.SymlinkAbsolutePathStrategy_DISALLOWED, // Same as RBE.
			SupportedCompressors: []repb.Compressor_Value{
				repb.Compressor_IDENTITY,
				repb.Compressor_ZSTD,
			},
			SplitBlobSupport:   s.config.EnableChunkedBlobs,
			SpliceBlobSupport:  s.config.EnableChunkedBlobs,
			FastCdc_2020Params: s.config.FastCDC_2020Params,
			RepMaxCdcParams:    s.config.RepMaxCDCParams,
		},
		ExecutionCapabilities: &repb.ExecutionCapabilities{
			DigestFunction:  values[0],
			DigestFunctions: values,
			ExecEnabled:     true,
			ExecutionPriorityCapabilities: &repb.PriorityCapabilities{
				Priorities: []*repb.PriorityCapabilities_PriorityRange{
					{
						MinPriority: 0,
						MaxPriority: 0,
					},
				},
			},
			SupportedNodeProperties: []string{"MTime", "UnixMode"},
		},
		LowApiVersion:  &semverpb.SemVer{Major: 2, Minor: 0},
		HighApiVersion: &semverpb.SemVer{Major: 2, Minor: 0}, // RBE does not support higher versions, so we don't either.
	}, nil
}
