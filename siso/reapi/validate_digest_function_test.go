// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package reapi

import (
	"testing"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// capWith builds ServerCapabilities advertising the given digest functions.
func capWith(fns ...rpb.DigestFunction_Value) *rpb.ServerCapabilities {
	return &rpb.ServerCapabilities{
		CacheCapabilities: &rpb.CacheCapabilities{
			DigestFunctions: fns,
		},
	}
}

func TestValidateDigestFunction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current rpb.DigestFunction_Value
		capa    *rpb.ServerCapabilities
		wantErr bool
	}{
		{
			name:    "empty list, sha256 client: old server, inferred ok",
			current: rpb.DigestFunction_SHA256,
			capa:    capWith(),
			wantErr: false,
		},
		{
			name:    "empty list, blake3 client: rejected",
			current: rpb.DigestFunction_BLAKE3,
			capa:    capWith(),
			wantErr: true,
		},
		{
			name:    "non-empty list without sha256, sha256 client: rejected",
			current: rpb.DigestFunction_SHA256,
			capa:    capWith(rpb.DigestFunction_BLAKE3),
			wantErr: true,
		},
		{
			name:    "list with blake3, blake3 client: ok",
			current: rpb.DigestFunction_BLAKE3,
			capa:    capWith(rpb.DigestFunction_SHA256, rpb.DigestFunction_BLAKE3),
			wantErr: false,
		},
		{
			name:    "list with sha256, sha256 client: ok",
			current: rpb.DigestFunction_SHA256,
			capa:    capWith(rpb.DigestFunction_SHA256, rpb.DigestFunction_BLAKE3),
			wantErr: false,
		},
		{
			name:    "exec enabled but only supports sha256, blake3 client: rejected",
			current: rpb.DigestFunction_BLAKE3,
			capa: &rpb.ServerCapabilities{
				CacheCapabilities: &rpb.CacheCapabilities{
					DigestFunctions: []rpb.DigestFunction_Value{rpb.DigestFunction_SHA256, rpb.DigestFunction_BLAKE3},
				},
				ExecutionCapabilities: &rpb.ExecutionCapabilities{
					ExecEnabled:     true,
					DigestFunctions: []rpb.DigestFunction_Value{rpb.DigestFunction_SHA256},
				},
			},
			wantErr: true,
		},
		{
			name:    "exec enabled and supports blake3, blake3 client: ok",
			current: rpb.DigestFunction_BLAKE3,
			capa: &rpb.ServerCapabilities{
				CacheCapabilities: &rpb.CacheCapabilities{
					DigestFunctions: []rpb.DigestFunction_Value{rpb.DigestFunction_SHA256, rpb.DigestFunction_BLAKE3},
				},
				ExecutionCapabilities: &rpb.ExecutionCapabilities{
					ExecEnabled:     true,
					DigestFunctions: []rpb.DigestFunction_Value{rpb.DigestFunction_SHA256, rpb.DigestFunction_BLAKE3},
				},
			},
			wantErr: false,
		},
		{
			name:    "exec enabled with only legacy singular blake3 field, blake3 client: ok",
			current: rpb.DigestFunction_BLAKE3,
			capa: &rpb.ServerCapabilities{
				CacheCapabilities: &rpb.CacheCapabilities{
					DigestFunctions: []rpb.DigestFunction_Value{rpb.DigestFunction_SHA256, rpb.DigestFunction_BLAKE3},
				},
				ExecutionCapabilities: &rpb.ExecutionCapabilities{
					ExecEnabled:    true,
					DigestFunction: rpb.DigestFunction_BLAKE3,
				},
			},
			wantErr: false,
		},
		{
			name:    "exec enabled with no digest functions at all, blake3 client: rejected",
			current: rpb.DigestFunction_BLAKE3,
			capa: &rpb.ServerCapabilities{
				CacheCapabilities: &rpb.CacheCapabilities{
					DigestFunctions: []rpb.DigestFunction_Value{rpb.DigestFunction_SHA256, rpb.DigestFunction_BLAKE3},
				},
				ExecutionCapabilities: &rpb.ExecutionCapabilities{
					ExecEnabled: true,
				},
			},
			wantErr: true,
		},
		{
			name:    "exec disabled, blake3 client with cache support: ok",
			current: rpb.DigestFunction_BLAKE3,
			capa: &rpb.ServerCapabilities{
				CacheCapabilities: &rpb.CacheCapabilities{
					DigestFunctions: []rpb.DigestFunction_Value{rpb.DigestFunction_SHA256, rpb.DigestFunction_BLAKE3},
				},
				ExecutionCapabilities: &rpb.ExecutionCapabilities{
					ExecEnabled: false,
				},
			},
			wantErr: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fn, err := digest.Lookup(tc.current)
			if err != nil {
				t.Fatalf("digest.Lookup(%v): %v", tc.current, err)
			}

			err = validateDigestFunction(fn, tc.capa)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("validateDigestFunction(%v) error = %v, wantErr = %v", tc.current, err, tc.wantErr)
			}
		})
	}
}
