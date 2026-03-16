// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"testing"
)

func TestEscape_Ninja(t *testing.T) {
	result := escapeStringNinja(`asdf: "$\bar`)
	if result != `asdf$:$ "$$\bar` {
		t.Errorf("got %q, want %q", result, `asdf$:$ "$$\bar`)
	}
}
