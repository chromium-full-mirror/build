// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build windows

package localexec

import "testing"

func TestConcurrencyLimit(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want int
	}{
		{n: maxWindowsConcurrency / 2, want: maxWindowsConcurrency / 2},
		{n: maxWindowsConcurrency, want: maxWindowsConcurrency},
		{n: maxWindowsConcurrency * 3, want: maxWindowsConcurrency},
	} {
		if got := concurrencyLimit(tc.n); got != tc.want {
			t.Errorf("concurrencyLimit(%d) = %d, want %d", tc.n, got, tc.want)
		}
	}
}
