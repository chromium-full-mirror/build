// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package atomicio_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"go.chromium.org/build/kajiya/atomicio"
)

func TestAtomicWriteFile(t *testing.T) {
	var want = bytes.Repeat([]byte{byte(32)}, 50)

	filePath := filepath.Join(t.TempDir(), "test")
	if err := atomicio.WriteFile(filePath, want); err != nil {
		t.Fatalf("Test subroutines not completed due to %v", err)
	}

	contents, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Fail to open the file %q: %v", filePath, err)
	}

	if !bytes.Equal(want, contents) {
		t.Errorf("WriteFile() got %v, want %v", contents, want)
	}
}
