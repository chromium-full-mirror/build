// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build linux

package main

import (
	"context"
	"fmt"
	"runtime"
)

type MachineType struct {
	CPUs              int
	MemGB             int
	DefaultDiskType   string
	DefaultDiskSizeGB int
}

var knownMachineTypes = map[string]MachineType{
	"n2-standard-8": {
		CPUs:              8,
		MemGB:             32,
		DefaultDiskType:   "pd-standard",
		DefaultDiskSizeGB: 300,
	},
	// cloudtop Medium
	"e2-custom-24-98304": {
		CPUs:              24,
		MemGB:             96,
		DefaultDiskType:   "pd-ssd",
		DefaultDiskSizeGB: 1024,
	},
}

// see https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html
func (m MachineType) Properties(ctx context.Context, sysMemGB, swapGB int) ([]string, error) {
	var p []string
	s, err := m.allowedCPUs(ctx)
	if err != nil {
		return nil, err
	}
	p = append(p, fmt.Sprintf("AllowedCPUs=%s", s))
	p = append(p, fmt.Sprintf("MemoryMax=%dG", m.MemGB-sysMemGB))
	p = append(p, fmt.Sprintf("MemoryHigh=%dG", m.MemGB-sysMemGB-swapGB))
	p = append(p, fmt.Sprintf("MemorySwapMax=%dG", swapGB))
	return p, nil
}

// allowedCPUs returns cpusets to use.
// see AllowedCPUs in https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html
// Calculate the correct set of CPUs, taking NUMA nodes into account.
func (m MachineType) allowedCPUs(ctx context.Context) (string, error) {
	switch runtime.NumCPU() {
	case 128:
		// P620 (Specialist): 64C/128T over 4 NUMA nodes (chiplets)
		switch m.CPUs {
		case 8:
			return "0,16,32,48,64,80,96,112", nil
		case 24:
			return "0-2,16-18,32-34,48-50,64-66,80-82,96-98,112-114", nil
		default:
			return "", fmt.Errorf("unknown target CPUs=%d", m.CPUs)
		}
	case 72:
		// P920 (Specialist): 36C/72T over 2 NUMA nodes (CPU sockets)
		switch m.CPUs {
		case 8:
			return "0-1,36-37,18-19,54-55", nil
		case 24:
			return "0-5,36-41,18-23,54-59", nil
		default:
			return "", fmt.Errorf("unknown target CPUs=%d", m.CPUs)
		}
	default:
		return "", fmt.Errorf("unknown host CPU (num_cpu=%d)", runtime.NumCPU())
	}
}
