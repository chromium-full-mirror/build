// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIsContextCanceledErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "context.Canceled",
			err:  context.Canceled,
			want: true,
		},
		{
			name: "wrapped context.Canceled",
			err:  fmt.Errorf("something: %w", context.Canceled),
			want: true,
		},
		{
			name: "grpc Canceled status",
			err:  status.Error(codes.Canceled, "context canceled"),
			want: true,
		},
		{
			name: "wrapped grpc Canceled status",
			err:  fmt.Errorf("find missing: %w", status.Error(codes.Canceled, "context canceled")),
			want: true,
		},
		{
			name: "deeply wrapped grpc Canceled (CAS upload path)",
			err: fmt.Errorf("failed to upload all foo: %w",
				fmt.Errorf("wait for digest=abc/123: %w",
					fmt.Errorf("find missing: %w",
						status.Error(codes.Canceled, "context canceled")))),
			want: true,
		},
		{
			name: "grpc DeadlineExceeded status",
			err:  status.Error(codes.DeadlineExceeded, "deadline exceeded"),
			want: false,
		},
		{
			name: "grpc Unavailable status",
			err:  status.Error(codes.Unavailable, "unavailable"),
			want: false,
		},
		{
			name: "unrelated error",
			err:  fmt.Errorf("something went wrong"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := isContextCanceledErr(tt.err), tt.want; got != want {
				t.Errorf("isContextCanceledErr(%v) = %v, want %v", tt.err, got, want)
			}
		})
	}
}
