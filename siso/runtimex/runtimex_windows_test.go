// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build windows

package runtimex

import (
	"fmt"
	"math/bits"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func removeOneCPU(mask uintptr) (uintptr, error) {
	if mask == 0 {
		return 0, fmt.Errorf("cpu affinity mask is empty")
	}
	maskbits := int(unsafe.Sizeof(mask) * 8)
	for i := range maskbits {
		newmask := mask & ^(1 << uint(i))
		if newmask != mask {
			return newmask, nil
		}
	}
	panic("not reached")
}

func resumeChildThread(k32 *syscall.DLL, childpid int) error {
	openThread := k32.MustFindProc("OpenThread")
	resumeThread := k32.MustFindProc("ResumeThread")
	thread32First := k32.MustFindProc("Thread32First")
	thread32Next := k32.MustFindProc("Thread32Next")

	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(snapshot)

	const threadSuspendResume = 0x0002

	type threadEntry32 struct {
		Size           uint32
		tUsage         uint32
		ThreadID       uint32
		OwnerProcessID uint32
		BasePri        int32
		DeltaPri       int32
		Flags          uint32
	}

	var te threadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	ret, _, err := thread32First.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&te)))
	if ret == 0 {
		return err
	}
	for te.OwnerProcessID != uint32(childpid) {
		ret, _, err = thread32Next.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&te)))
		if ret == 0 {
			return err
		}
	}
	h, _, err := openThread.Call(threadSuspendResume, 1, uintptr(te.ThreadID))
	if h == 0 {
		return err
	}
	defer syscall.Close(syscall.Handle(h))

	ret, _, err = resumeThread.Call(h)
	if ret == 0xffffffff {
		return err
	}
	return nil
}

func TestGOMAXPROCSAffinity(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		fmt.Fprintf(os.Stderr, "%d", runtime.GOMAXPROCS(0))
		os.Exit(0)
	}

	if getCPUCount() <= 1 {
		t.Skip("skipping test on single-CPU system")
	}

	const (
		createSuspended  = 0x00000004
		processAllAccess = syscall.STANDARD_RIGHTS_REQUIRED | syscall.SYNCHRONIZE | 0xfff
	)

	k32 := syscall.MustLoadDLL("kernel32.dll")
	getProcessAffinityMask := k32.MustFindProc("GetProcessAffinityMask")
	setProcessAffinityMask := k32.MustFindProc("SetProcessAffinityMask")

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	var newmask uintptr
	cmd := exec.Command(exe, "-test.run=^TestGOMAXPROCSAffinity$")
	cmd.Env = append(os.Environ(), "GOMAXPROCS=", "GO_WANT_HELPER_PROCESS=1")
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createSuspended}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := cmd.Wait()
		childOutput := buf.String()
		if err != nil {
			t.Fatalf("child failed: %v: %v", err, childOutput)
		}
		// The child can only run on the CPUs in newmask. On a system with
		// more than one processor group, setting the mask also confines it
		// to a single group.
		if want := bits.OnesCount64(uint64(newmask)); childOutput != strconv.Itoa(want) {
			t.Fatalf("child GOMAXPROCS = %q, want %d (affinity mask 0x%x)", childOutput, want, newmask)
		}
	}()

	defer func() {
		if err := resumeChildThread(k32, cmd.Process.Pid); err != nil {
			t.Fatal(err)
		}
	}()

	ph, err := syscall.OpenProcess(processAllAccess, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(ph)

	var mask, sysmask uintptr
	ret, _, err := getProcessAffinityMask.Call(uintptr(ph), uintptr(unsafe.Pointer(&mask)), uintptr(unsafe.Pointer(&sysmask)))
	if ret == 0 {
		t.Fatal(err)
	}

	newmask, err = removeOneCPU(mask)
	if err != nil {
		t.Fatal(err)
	}

	ret, _, err = setProcessAffinityMask.Call(uintptr(ph), newmask)
	if ret == 0 {
		t.Fatal(err)
	}
	ret, _, err = getProcessAffinityMask.Call(uintptr(ph), uintptr(unsafe.Pointer(&mask)), uintptr(unsafe.Pointer(&sysmask)))
	if ret == 0 {
		t.Fatal(err)
	}
	if newmask != mask {
		t.Fatalf("SetProcessAffinityMask didn't set newmask of 0x%x. Current mask is 0x%x.", newmask, mask)
	}
}

func TestGOMAXPROCSProcessorGroups(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		fmt.Fprintf(os.Stderr, "%d", runtime.GOMAXPROCS(0))
		os.Exit(0)
	}

	k32 := syscall.MustLoadDLL("kernel32.dll")
	getActiveProcessorCount := k32.MustFindProc("GetActiveProcessorCount")
	getActiveProcessorGroupCount := k32.MustFindProc("GetActiveProcessorGroupCount")
	createJobObjectW := k32.MustFindProc("CreateJobObjectW")
	setInformationJobObject := k32.MustFindProc("SetInformationJobObject")
	assignProcessToJobObject := k32.MustFindProc("AssignProcessToJobObject")

	ngroups, _, _ := getActiveProcessorGroupCount.Call()
	if uint16(ngroups) < 2 {
		t.Skip("skipping test: system has a single processor group")
	}
	// Before Windows 11 (10.0.22000) and Windows Server 2022 (10.0.20348),
	// each process runs in a single processor group, so the child can't span
	// groups. All older Windows versions have lower builds than both. See
	// https://learn.microsoft.com/en-us/windows/win32/procthread/processor-groups.
	if major, _, build := windows.RtlGetNtVersionNumbers(); major < 10 || build < 20348 {
		t.Skipf("skipping test: Windows %d.0.%d runs each process in a single processor group", major, build)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	const (
		createSuspended  = 0x00000004
		processAllAccess = syscall.STANDARD_RIGHTS_REQUIRED | syscall.SYNCHRONIZE | 0xfff
	)

	// runInJob runs a child in a job object limited to affinity and
	// returns the child's runtime.GOMAXPROCS(0).
	runInJob := func(t *testing.T, affinity []groupAffinity) string {
		t.Helper()
		job, _, err := createJobObjectW.Call(0, 0)
		if job == 0 {
			t.Fatalf("CreateJobObject: %v", err)
		}
		defer syscall.CloseHandle(syscall.Handle(job))
		ret, _, err := setInformationJobObject.Call(job, windows.JobObjectGroupInformationEx, uintptr(unsafe.Pointer(&affinity[0])), uintptr(len(affinity))*unsafe.Sizeof(affinity[0]))
		if ret == 0 {
			t.Fatalf("SetInformationJobObject: %v", err)
		}

		cmd := exec.Command(exe, "-test.run=^TestGOMAXPROCSProcessorGroups$")
		cmd.Env = append(os.Environ(), "GOMAXPROCS=", "GO_WANT_HELPER_PROCESS=1")
		var buf strings.Builder
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createSuspended}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		ph, err := syscall.OpenProcess(processAllAccess, false, uint32(cmd.Process.Pid))
		if err != nil {
			cmd.Process.Kill()
			t.Fatalf("OpenProcess: %v", err)
		}
		ret, _, err = assignProcessToJobObject.Call(job, uintptr(ph))
		syscall.CloseHandle(ph)
		if ret == 0 {
			cmd.Process.Kill()
			t.Fatalf("AssignProcessToJobObject: %v", err)
		}
		if err := resumeChildThread(k32, cmd.Process.Pid); err != nil {
			cmd.Process.Kill()
			t.Fatalf("resumeChildThread: %v", err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child failed: %v: %v", err, buf.String())
		}
		return buf.String()
	}

	t.Run("AllGroups", func(t *testing.T) {
		var affinity []groupAffinity
		want := 0
		for g := range uint16(ngroups) {
			// A mask can't include processors that the group doesn't have,
			// or more than fit in a uintptr.
			n, _, _ := getActiveProcessorCount.Call(uintptr(g))
			cpus := min(int(uint32(n)), bits.UintSize)
			mask := ^uintptr(0) >> (bits.UintSize - cpus)
			affinity = append(affinity, groupAffinity{Mask: mask, Group: g})
			want += cpus
		}
		if got := runInJob(t, affinity); got != strconv.Itoa(want) {
			t.Errorf("child GOMAXPROCS = %q, want %d (affinity %+v)", got, want, affinity)
		}
	})

	t.Run("TwoCPUsPerGroup", func(t *testing.T) {
		affinity := []groupAffinity{{Mask: 0b11, Group: 0}, {Mask: 0b11, Group: 1}}
		if got := runInJob(t, affinity); got != "4" {
			t.Errorf("child GOMAXPROCS = %q, want 4 (affinity %+v)", got, affinity)
		}
	})

	t.Run("EnvOverride", func(t *testing.T) {
		cmd := exec.Command(exe, "-test.run=^TestGOMAXPROCSProcessorGroups$")
		cmd.Env = append(os.Environ(), "GOMAXPROCS=3", "GO_WANT_HELPER_PROCESS=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child failed: %v: %s", err, out)
		}
		if got := string(out); got != "3" {
			t.Errorf("child GOMAXPROCS with GOMAXPROCS=3 = %q, want 3", got)
		}
	})
}
