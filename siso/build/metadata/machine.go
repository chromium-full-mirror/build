// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package metadata

import (
	"runtime"

	"github.com/klauspost/cpuid/v2"
)

// MachineInfo represents information about the machine that the build was invoked on.
type MachineInfo struct {
	// Platform reports platform information of the machine that the build was invoked on.
	Platform PlatformInfo `json:"platform"`
	// CPU reports CPU information, e.g. brand name and vendor string.
	CPU CPUInfo `json:"cpu"`
}

// GatherMachineInfo gathers and returns machine information of the build environment.
func GatherMachineInfo() MachineInfo {
	return MachineInfo{
		Platform: PlatformInfo{
			OS:           runtime.GOOS,
			Architecture: runtime.GOARCH,
		},
		CPU: CPUInfo{
			BrandName:    cpuid.CPU.BrandName,
			VendorString: cpuid.CPU.VendorString,
		},
	}
}

// PlatformInfo reports platform information of the machine that the build was invoked on.
type PlatformInfo struct {
	// OS reports the host's operating system.
	//
	// It is populated with similar semantics to the "os" field in the OCI Image Configuration specification.
	// Hence, consumers SHOULD understand values listed in the Go Language document for GOOS.
	OS string `json:"os"`
	// Architecture reports the host's architecture.
	//
	// It is populated with similar semantics to the "architecture" field in the OCI Image Configuration specification.
	// Hence, consumers SHOULD understand values listed in the Go Language document for GOARCH.
	Architecture string `json:"architecture"`
}

// CPUInfo reports CPU information.
type CPUInfo struct {
	// BrandName is the brand name reported by the CPU, e.g. "Intel(R) Xeon(R) CPU @ 2.20GHz".
	BrandName string `json:"brand"`
	// VendorString is the raw vendor string reported by the CPU, e.g. "GenuineIntel".
	VendorString string `json:"vendor"`
}
