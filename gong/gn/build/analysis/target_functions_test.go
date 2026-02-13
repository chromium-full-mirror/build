// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"bytes"
	"errors"
	"testing"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

type mockInput struct {
	displayName string
	contents    string
}

func (m mockInput) DisplayName() string { return m.displayName }
func (m mockInput) Contents() []byte    { return []byte(m.contents) }
func (m mockInput) Equal(other syntax.InputSource) bool {
	return bytes.Equal(m.Contents(), other.Contents())
}

func TestSchema_Run(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		wantErr any
	}{
		{
			name:  "empty",
			input: `source_set("foo") {}`,
		},
		{
			name: "normal",
			input: `source_set("foo") {
  sources = [ "foo.cc" ]
  deps = [ "//bar:baz", "//bar:qux" ]
}`,
		},
		{
			name:    "no name",
			input:   `source_set() {}`,
			wantErr: &resolve.ArgumentCountError{},
		},
		{
			name: "unused",
			input: `source_set("foo") {
  unused = 5
}`,
			wantErr: &resolve.UnusedVarError{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokens, err := syntax.Tokenize(mockInput{
				displayName: tc.name,
				contents:    tc.input,
			})
			if err != nil {
				t.Fatalf("failed to tokenize: %v", err)
			}
			root, err := parse.Parse(tokens)
			if err != nil {
				t.Fatalf("failed to parse: %v", err)
			}

			_, err = resolve.ExecuteNode(root, resolve.NewScope(
				&scopeContext{
					settings:      NewSettings(&environment.BuildSettings{}),
					sourceDir:     mustDir(t, "//"),
					itemCollector: func(i Item) {},
				},
				nil,
				map[string]resolve.FunctionInfo{
					"source_set": &SourceSetSchema,
				},
			))

			wantErr := tc.wantErr != nil
			gotErr := err != nil

			if gotErr != wantErr {
				t.Errorf("execute got err=%v (%T), wantErr %T", err, err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if !errors.As(err, &tc.wantErr) {
					t.Errorf("execute got err=%v (%T), wantErr %T", err, err, tc.wantErr)
				}
			}
		})
	}
}
