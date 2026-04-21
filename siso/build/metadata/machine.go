// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package metadata

// MachineInfo represents information about the machine that the build was invoked on.
type MachineInfo struct {
	// CPU reports CPU information, e.g. brand name and vendor string.
	CPU CPUInfo `json:"cpu"`
}

// CPUInfo reports CPU information.
type CPUInfo struct {
	// BrandName is the brand name reported by the CPU, e.g. "Intel(R) Xeon(R) CPU @ 2.20GHz".
	BrandName string `json:"brand"`
	// VendorString is the raw vendor string reported by the CPU, e.g. "GenuineIntel".
	VendorString string `json:"vendor"`
}
