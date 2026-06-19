// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"testing"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

func TestGetEnvFunction(t *testing.T) {
	t.Setenv("GONG_TEST_ENV_VAR", "hello_world")
	t.Setenv("gong_lowercase_var", "lowercase_val")

	for _, tc := range []struct {
		name    string
		args    []resolve.Value
		wantVal string
		wantErr bool
	}{
		{
			name: "exactupper",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("GONG_TEST_ENV_VAR"),
			},
			wantVal: "hello_world",
		},
		{
			name: "exactlower",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("gong_lowercase_var"),
			},
			wantVal: "lowercase_val",
		},
		{
			name: "fallbacklower",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("gong_test_env_var"),
			},
			wantVal: "hello_world",
		},
		{
			name: "fallbackupper",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("GONG_LOWERCASE_VAR"),
			},
			wantVal: "lowercase_val",
		},
		{
			name: "missing",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("GONG_NON_EXISTENT_VAR"),
			},
			wantVal: "",
		},
		{
			name:    "zeroargs",
			args:    []resolve.Value{},
			wantErr: true,
		},
		{
			name: "multipleargs",
			args: []resolve.Value{
				resolve.NewOriginlessStringValue("A"),
				resolve.NewOriginlessStringValue("B"),
			},
			wantErr: true,
		},
		{
			name: "invalidarg",
			args: []resolve.Value{
				resolve.NewOriginlessIntegerValue(123),
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			val, err := getenvFunction{}.Run(
				nil,
				&parse.FunctionCallNode{
					Function: syntax.MakeToken(syntax.TokenIdentifier, "getenv"),
				},
				tc.args,
			)

			gotErr := err != nil
			if gotErr != tc.wantErr {
				t.Fatalf("f.Run() got err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}

			got, err := resolve.AsValue[*resolve.StringValue](val)
			if err != nil {
				t.Fatalf("expected string value: %v", err)
			}
			if got.RawGNString() != tc.wantVal {
				t.Errorf("getenv() = %q; want %q", got.RawGNString(), tc.wantVal)
			}
		})
	}
}
