// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build windows

package cpu

import (
	"golang.org/x/sys/windows"
)

const allProcessorGroups = 0xFFFF

// On Windows, runtime.NumCPU() only returns the information for a single Processor Group (up to 64).
// logicalCores() uses GetActiveProcessorCount to get cpu counts from all Processor Groups.
// See the solution in kubernetes.
// https://github.com/kubernetes/kubernetes/blob/a4b8a3b2e33a3b591884f69b64f439e6b880dc40/pkg/kubelet/winstats/perfcounter_nodestats_windows.go#L205
func logicalCores() int {
	return int(windows.GetActiveProcessorCount(allProcessorGroups))
}
