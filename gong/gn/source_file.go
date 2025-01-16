// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package gn

import (
	"fmt"
	"strings"
	"unique"
)

// SourceFile represents a file within the source tree. Always begins in a slash, never
// ends in one.
type SourceFile struct {
	value unique.Handle[string]
}

func makeSourceFile(value string) (SourceFile, error) {
	if !strings.HasPrefix(value, "/") {
		return SourceFile{}, fmt.Errorf("should start with slash")
	}
	if endsWithSlash(value) {
		return SourceFile{}, fmt.Errorf("should not end with slash")
	}
	return SourceFile{
		value: unique.Make(NormalizePath(value)),
	}, nil
}
