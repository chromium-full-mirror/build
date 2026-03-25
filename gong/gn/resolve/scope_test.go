// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package resolve

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

func TestScope_NonRecursiveMergeTo(t *testing.T) {
	sourceNode := &parse.LiteralNode{
		Token: syntax.MakeToken(syntax.TokenString, "origin"),
	}

	sourceScope := &Scope{
		values: map[string]record{
			"v":        {false, &StringValue{value: "hello", origin: &parse.LiteralNode{}}},
			"_private": {false, &StringValue{value: "hello", origin: &parse.LiteralNode{}}},
		},
	}

	for _, tc := range []struct {
		name          string
		source        *Scope
		dest          *Scope
		options       ScopeMergeOptions
		wantDestValue Value
		wantErr       bool
	}{
		{
			name:   "Detect value collision",
			source: sourceScope,
			dest: &Scope{
				values: map[string]record{
					"v": {false, &StringValue{value: "goodbye"}},
				},
			},
			options: ScopeMergeOptions{
				SourceNode: sourceNode,
			},
			wantErr: true,
		},
		{
			name:   "Clobber colliding values",
			source: sourceScope,
			dest: &Scope{
				values: map[string]record{
					"v": {false, &StringValue{value: "goodbye"}},
				},
			},
			options: ScopeMergeOptions{
				DestinationClobber: true,
			},
			wantDestValue: &StringValue{value: "hello"},
		},
		{
			name:   "No error on same value",
			source: sourceScope,
			dest: &Scope{
				values: map[string]record{
					"v": {false, &StringValue{value: "hello"}},
				},
			},
			options:       ScopeMergeOptions{},
			wantDestValue: &StringValue{value: "hello"},
		},
		{
			name:   "No error on empty source",
			source: &Scope{},
			dest: &Scope{
				values: map[string]record{
					"v": {false, &StringValue{value: "hello"}},
				},
			},
			options: ScopeMergeOptions{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.source.NonRecursiveMergeTo(tc.dest, tc.options)

			gotErr := err != nil

			if gotErr != tc.wantErr {
				t.Fatalf("NonRecursiveMergeTo() got err = %v (%T), want %T", err, err, tc.wantErr)
			}

			if gotErr {
				var wantErr *ScopeMergeError
				if !errors.As(err, &wantErr) {
					t.Errorf("NonRecursiveMergeTo() got err=%v (%T), want %T", err, err, wantErr)
				}
			} else if tc.wantDestValue != nil {
				got := tc.dest.Value("v", false)
				if diff := cmp.Diff(tc.wantDestValue, got); diff != "" {
					t.Errorf("NonRecursiveMergeTo() value mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}

	t.Run("Copy private values", func(t *testing.T) {
		dest := NewScope(nil, nil)
		err := sourceScope.NonRecursiveMergeTo(dest, ScopeMergeOptions{})
		if err != nil {
			t.Fatalf("NonRecursiveMergeTo() failed: %v", err)
		}
		if !dest.Value("_private", false).Equal(&StringValue{value: "hello"}) {
			t.Error("expected private var to be copied")
		}
	})

	t.Run("Skip private values", func(t *testing.T) {
		dest := NewScope(nil, nil)
		err := sourceScope.NonRecursiveMergeTo(dest, ScopeMergeOptions{SkipPrivateVars: true})
		if err != nil {
			t.Fatalf("NonRecursiveMergeTo() failed: %v", err)
		}
		if dest.Value("_private", false) != nil {
			t.Error("expected private var to be skipped")
		}
	})

	t.Run("Excluded values", func(t *testing.T) {
		dest := NewScope(nil, nil)
		err := sourceScope.NonRecursiveMergeTo(dest, ScopeMergeOptions{
			ExcludedValues: map[string]struct{}{
				"v": {},
			},
		})
		if err != nil {
			t.Fatalf("NonRecursiveMergeTo() failed: %v", err)
		}
		if dest.Value("v", false) != nil {
			t.Error("expected 'v' to be excluded")
		}
		if dest.Value("_private", false) == nil {
			t.Error("expected '_private' to be preserved")
		}
	})

	t.Run("Don't mark used", func(t *testing.T) {
		dest := NewScope(nil, nil)
		err := sourceScope.NonRecursiveMergeTo(dest, ScopeMergeOptions{})
		if err != nil {
			t.Fatalf("NonRecursiveMergeTo() failed: %v", err)
		}
		if err := dest.CheckForUnusedVars(); err == nil {
			t.Fatal("CheckForUnusedVars() expected error, got nil")
		}
	})

	t.Run("Mark dest used", func(t *testing.T) {
		dest := NewScope(nil, nil)
		err := sourceScope.NonRecursiveMergeTo(dest, ScopeMergeOptions{DestinationMarkUsed: true})
		if err != nil {
			t.Fatalf("NonRecursiveMergeTo() failed: %v", err)
		}
		if err := dest.CheckForUnusedVars(); err != nil {
			t.Fatalf("CheckForUnusedVars() unexpected error: %v", err)
		}
	})
}
