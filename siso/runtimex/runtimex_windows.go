// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build windows

package runtimex

import (
	"math/bits"
	"os"
	"runtime"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

// https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-group_affinity
type groupAffinity struct {
	Mask     uintptr
	Group    uint16
	Reserved [3]uint16
}

var (
	kernel32                    = windows.NewLazySystemDLL("kernel32.dll")
	procGetProcessAffinityMask  = kernel32.NewProc("GetProcessAffinityMask")
	procGetProcessGroupAffinity = kernel32.NewProc("GetProcessGroupAffinity")
)

func init() {
	if v, err := strconv.Atoi(os.Getenv("GOMAXPROCS")); err == nil && v > 0 {
		return
	}
	if n := int(getCPUCount()); n > runtime.NumCPU() {
		runtime.GOMAXPROCS(n)
	}
}

// getCPUCount mirrors runtime.getCPUCount from https://go.dev/cl/857241.
func getCPUCount() int32 {
	// GetProcessAffinityMask and GetSystemInfo only cover a single processor
	// group, whereas processes span all groups by default since Windows 11.
	var groups [64]uint16
	count := uint16(len(groups))
	if r1, _, _ := procGetProcessGroupAffinity.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&groups[0]))); r1 != 0 && count > 1 {
		var n int32
		// A job object can limit the process to some processors in each group.
		var job [64]groupAffinity
		var size uint32
		if windows.QueryInformationJobObject(0, windows.JobObjectGroupInformationEx, uintptr(unsafe.Pointer(&job[0])), uint32(unsafe.Sizeof(job)), &size) == nil && size != 0 {
			for _, g := range job[:size/uint32(unsafe.Sizeof(job[0]))] {
				active := int32(windows.GetActiveProcessorCount(g.Group))
				n += min(int32(bits.OnesCount64(uint64(g.Mask))), active)
			}
		} else {
			for _, g := range groups[:count] {
				n += int32(windows.GetActiveProcessorCount(g))
			}
		}
		if n != 0 {
			return n
		}
	}

	var mask, sysmask uintptr
	if r1, _, _ := procGetProcessAffinityMask.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&mask)), uintptr(unsafe.Pointer(&sysmask))); r1 != 0 {
		if n := int32(bits.OnesCount64(uint64(mask))); n != 0 {
			return n
		}
	}
	return int32(runtime.NumCPU())
}
