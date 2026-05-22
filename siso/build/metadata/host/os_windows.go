// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build windows

package host

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type osVersionInfoEx struct {
	dwOSVersionInfoSize uint32
	dwMajorVersion      uint32
	dwMinorVersion      uint32
	dwBuildNumber       uint32
	dwPlatformId        uint32
	szCSDVersion        [128]uint16
	wServicePackMajor   uint16
	wServicePackMinor   uint16
	wSuiteMask          uint16
	wProductType        byte
	wReserved           byte
}

func osVersion() (string, error) {
	ntdll := windows.NewLazySystemDLL("ntdll.dll")
	procRtlGetVersion := ntdll.NewProc("RtlGetVersion")

	err := procRtlGetVersion.Find()
	if err != nil {
		return "", fmt.Errorf("failed to find RtlGetVersion: %w", err)
	}

	var osvi osVersionInfoEx
	osvi.dwOSVersionInfoSize = uint32(unsafe.Sizeof(osvi))
	r1, _, err := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&osvi)))
	if r1 == 0 {
		if err != nil && err != syscall.Errno(0) {
			return "", fmt.Errorf("RtlGetVersion failed: %w", err)
		}
		return "", fmt.Errorf("RtlGetVersion returned 0 despite no error")
	}

	// Format version as Major.Minor.Build (e.g., 10.0.22631)
	// NOTE: Be aware that Windows 11 reports Major=10, but Build >= 22000
	return fmt.Sprintf("%d.%d.%d", osvi.dwMajorVersion, osvi.dwMinorVersion, osvi.dwBuildNumber), nil
}

func kernelVersion() (string, error) {
	return "", ErrUnsupportedOS
}
