// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"fmt"
	"strings"

	"github.com/klauspost/cpuid/v2"
	"github.com/shirou/gopsutil/v4/mem"
)

// cpuInfo returns a string with information about the CPU.
func cpuInfo() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "family=%x model=%x stepping=%x ", cpuid.CPU.Family, cpuid.CPU.Model, cpuid.CPU.Stepping)
	fmt.Fprintf(&sb, "brand=%q vendor=%q ", cpuid.CPU.BrandName, cpuid.CPU.VendorString)
	fmt.Fprintf(&sb, "physicalCores=%d threadsPerCore=%d logicalCores=%d ", cpuid.CPU.PhysicalCores, cpuid.CPU.ThreadsPerCore, cpuid.CPU.LogicalCores)
	fmt.Fprintf(&sb, "vm=%t features=%s", cpuid.CPU.VM(), cpuid.CPU.FeatureSet())
	return sb.String()
}

// memInfo returns a string with information about the memory.
func memInfo() string {
	v, _ := mem.VirtualMemory()
	return fmt.Sprintf("total=%dMiB, available=%dMiB, usedpercent=%.2f%%", v.Total/1024/1024, v.Available/1024/1024, v.UsedPercent)
}
