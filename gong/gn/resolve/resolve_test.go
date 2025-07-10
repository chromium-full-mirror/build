// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/parse"
)

func TestExecuteNode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		node    parse.ParseNode
		scope   *Scope
		want    Value
		wantErr bool
	}{
		{
			name:    "blockcomment_nothing",
			node:    &parse.BlockCommentNode{},
			scope:   &Scope{},
			wantErr: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExecuteNode(tc.node, tc.scope)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("ExecuteNode(%T, %T) = %q, %v; want %q; err=%v", tc.node, tc.scope, got, err, tc.want, tc.wantErr)
				return
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ExecuteNode(%T, %T); diff -want +got:\n%s", tc.node, tc.scope, diff)
			}
		})
	}
}
