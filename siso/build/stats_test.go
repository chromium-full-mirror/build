// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestStatsUpdate_CacheDisabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    StepMetric
		want Stats
	}{
		{
			name: "local_cache_disabled",
			m:    StepMetric{IsLocal: true, CacheDisabled: true},
			want: Stats{
				Total:              1,
				Done:               1,
				Local:              1,
				CacheDisabled:      1,
				LocalCacheDisabled: 1,
			},
		},
		{
			name: "remote_cache_disabled",
			m:    StepMetric{IsRemote: true, CacheDisabled: true},
			want: Stats{
				Total:               1,
				Done:                1,
				Remote:              1,
				CacheDisabled:       1,
				RemoteCacheDisabled: 1,
			},
		},
		{
			name: "no_exec_ignores_cache_disabled",
			m:    StepMetric{NoExec: true, CacheDisabled: true},
			want: Stats{
				Total:  1,
				Done:   1,
				NoExec: 1,
			},
		},
		{
			name: "skipped_ignores_cache_disabled",
			m:    StepMetric{skip: true, CacheDisabled: true},
			want: Stats{
				Total:   1,
				Done:    1,
				Skipped: 1,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStats(1)
			m := tc.m
			s.update(t.Context(), &m, false)
			got := s.stats()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("stats.update() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
