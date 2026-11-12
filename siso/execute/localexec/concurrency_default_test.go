// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build !windows

package localexec

import "testing"

func TestConcurrencyLimit(t *testing.T) {
	for _, n := range []int{32, 64, 192} {
		if got := concurrencyLimit(n); got != n {
			t.Errorf("concurrencyLimit(%d) = %d, want %d", n, got, n)
		}
	}
}
