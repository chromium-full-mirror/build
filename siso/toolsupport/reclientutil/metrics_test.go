// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package reclientutil

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"go.chromium.org/build/siso/build"
)

func TestCompletionStatus(t *testing.T) {
	stats := build.Stats{
		Done:               105,
		NoExec:             5,
		CacheHit:           50,
		CacheHitEarly:      30,
		CacheHitLate:       20,
		RacingRemote:       25,
		RacingLocal:        15,
		Remote:             10,
		LocalFallback:      2,
		Local:              38,
		CacheDisabled:      5,
		LocalCacheDisabled: 5,
		Fail:               1,
		TwoPhaseCacheHit:   10,
	}

	stat := completionStatus(stats)
	if stat.Name != "CompletionStatus" {
		t.Errorf("stat.Name = %q, want CompletionStatus", stat.Name)
	}

	counts := make(map[string]int64)
	for _, v := range stat.CountsByValue {
		counts[v.Name] = v.Count
	}

	expected := map[string]int64{
		"STATUS_CACHE_HIT":           50,
		"STATUS_TWO_PHASE_CACHE_HIT": 10,
		"STATUS_CACHE_HIT_EARLY":     30,
		"STATUS_CACHE_HIT_LATE":      20,
		"STATUS_RACING_REMOTE":       25,
		"STATUS_RACING_LOCAL":        15,
		"STATUS_REMOTE_EXECUTION":    10,
		"STATUS_LOCAL_FALLBACK":      2,
		"STATUS_LOCAL_EXECUTION":     38,
		"STATUS_NO_EXEC":             5,
		"STATUS_CACHE_DISABLED":      5,
		"STATUS_NON_ZERO_EXIT":       1,
	}

	if diff := cmp.Diff(expected, counts); diff != "" {
		t.Errorf("completionStatus mismatch (-want +got):\n%s", diff)
	}
}

func TestRBEBuildMetrics(t *testing.T) {
	for _, tc := range []struct {
		name          string
		stats         build.Stats
		wantRecords   int64
		wantRemoteCHR float64
		wantLocalCHR  float64
		wantBuildCHR  float64
	}{
		{
			name: "mixed_actions",
			stats: build.Stats{
				Total:            100,
				Done:             100,
				Skipped:          10,
				CacheHit:         45,
				CacheHitEarly:    30,
				CacheHitLate:     15,
				RacingRemote:     20,
				RacingLocal:      15,
				LocalFallback:    2,
				TwoPhaseCacheHit: 10,
				Remote:           5,
				Local:            40,
			},
			wantRecords:   90,
			wantRemoteCHR: 0.8653846,
			wantLocalCHR:  0.2,
			wantBuildCHR:  0.50,
		},
		{
			name: "excludes_no_exec_from_chr",
			stats: build.Stats{
				Total:            100,
				Done:             100,
				Skipped:          10,
				NoExec:           10,
				CacheHit:         80,
				CacheHitEarly:    60,
				TwoPhaseCacheHit: 20,
			},
			wantRecords:   90,
			wantRemoteCHR: 1.0,
			wantLocalCHR:  1.0,
			wantBuildCHR:  1.0,
		},
		{
			name: "excludes_local_cache_disabled",
			stats: build.Stats{
				Total:              120,
				Done:               120,
				Skipped:            10,
				NoExec:             5,
				CacheHit:           80,
				CacheHitEarly:      60,
				TwoPhaseCacheHit:   20,
				Local:              25,
				CacheDisabled:      25,
				LocalCacheDisabled: 25,
			},
			// NumRecords keeps all non-skipped actions (120 - 10 = 110).
			wantRecords:   110,
			wantRemoteCHR: 1.0,
			wantLocalCHR:  1.0,
			wantBuildCHR:  1.0,
		},
		{
			name: "excludes_remote_cache_disabled",
			stats: build.Stats{
				Total:               50,
				Done:                50,
				CacheHit:            30,
				CacheHitEarly:       30,
				Remote:              20,
				CacheDisabled:       10,
				RemoteCacheDisabled: 10,
			},
			wantRecords:   50,
			wantRemoteCHR: 0.75, // 30 / (30 + 20 - 10)
			wantLocalCHR:  0,
			wantBuildCHR:  0.75, // 30 / (50 - 10)
		},
		{
			name: "all_cache_disabled",
			stats: build.Stats{
				Total:              10,
				Done:               10,
				Local:              10,
				CacheDisabled:      10,
				LocalCacheDisabled: 10,
			},
			wantRecords:   10,
			wantRemoteCHR: 0,
			wantLocalCHR:  0,
			wantBuildCHR:  0,
		},
		{
			name: "no_exec_only",
			stats: build.Stats{
				Total:  5,
				Done:   5,
				NoExec: 5,
			},
			wantRecords:   5,
			wantRemoteCHR: 0,
			wantLocalCHR:  0,
			wantBuildCHR:  0,
		},
		{
			name:          "empty_stats",
			stats:         build.Stats{},
			wantRecords:   0,
			wantRemoteCHR: 0,
			wantLocalCHR:  0,
			wantBuildCHR:  0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metrics := RBEBuildMetrics("build-123", "v1.0", 12*time.Second, tc.stats)
			if metrics.NumRecords != tc.wantRecords {
				t.Errorf("NumRecords = %d, want %d", metrics.NumRecords, tc.wantRecords)
			}
			if !cmp.Equal(metrics.RemoteCacheHitRatio, tc.wantRemoteCHR, cmpopts.EquateApprox(0, 0.000001)) {
				t.Errorf("RemoteCacheHitRatio = %f, want %f", metrics.RemoteCacheHitRatio, tc.wantRemoteCHR)
			}
			if !cmp.Equal(metrics.TwoPhaseCacheHitRatio, tc.wantLocalCHR, cmpopts.EquateApprox(0, 0.000001)) {
				t.Errorf("TwoPhaseCacheHitRatio = %f, want %f", metrics.TwoPhaseCacheHitRatio, tc.wantLocalCHR)
			}
			if !cmp.Equal(metrics.BuildCacheHitRatio, tc.wantBuildCHR, cmpopts.EquateApprox(0, 0.000001)) {
				t.Errorf("BuildCacheHitRatio = %f, want %f", metrics.BuildCacheHitRatio, tc.wantBuildCHR)
			}
			if len(metrics.Stats) != 1 {
				t.Fatalf("len(metrics.Stats) = %d, want 1", len(metrics.Stats))
			}
			if metrics.Stats[0].Name != "CompletionStatus" {
				t.Errorf("stat.Name = %q, want CompletionStatus", metrics.Stats[0].Name)
			}
		})
	}
}
