// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"bytes"
	"io"
	"os"
	"testing"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

func TestPrintFunction(t *testing.T) {
	f := printFunction{}

	t.Run("default", func(t *testing.T) {
		oldStdout := os.Stdout
		t.Cleanup(func() {
			os.Stdout = oldStdout
		})
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("failed to create pipe: %v", err)
		}
		t.Cleanup(func() {
			w.Close()
		})
		os.Stdout = w

		_, err = f.Run(
			resolve.NewScope(
				&scopeContext{
					settings: NewSettings(&environment.BuildSettings{}, NewImportManager(&fs.InputFileManager{})),
				},
				nil,
			),
			&parse.FunctionCallNode{
				Function: syntax.MakeToken(syntax.TokenIdentifier, "print"),
			},
			[]resolve.Value{
				// Should result in "stdout test\n".
				resolve.NewOriginlessStringValue("stdout"),
				resolve.NewOriginlessStringValue("test"),
			},
		)
		w.Close()

		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}
		var buf bytes.Buffer
		_, err = io.Copy(&buf, r)
		if err != nil {
			t.Fatalf("failed to read from pipe: %v", err)
		}
		want := "stdout test\n"
		if buf.String() != want {
			t.Errorf("printed = %q; want %q", buf.String(), want)
		}
	})

	t.Run("override", func(t *testing.T) {
		var printed string
		buildSettings := &environment.BuildSettings{
			PrintCallback: func(msg string) {
				printed = msg
			},
		}

		_, err := f.Run(
			resolve.NewScope(
				&scopeContext{
					settings: NewSettings(buildSettings, NewImportManager(&fs.InputFileManager{})),
				},
				nil,
			),
			&parse.FunctionCallNode{
				Function: syntax.MakeToken(syntax.TokenIdentifier, "print"),
			},
			[]resolve.Value{
				// Should result in "hello 42 world\n".
				resolve.NewOriginlessStringValue("hello"),
				resolve.NewOriginlessIntegerValue(42),
				resolve.NewOriginlessStringValue("world"),
			},
		)
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}

		want := "hello 42 world\n"
		if printed != want {
			t.Errorf("printed = %q; want %q", printed, want)
		}
	})
}
