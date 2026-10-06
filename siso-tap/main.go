// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Binary siso-tap is simple tap command.
// siso-tap uses seccomp filter to capture file access of the command
// and can be used for experiments of tapping in two phase caching
// on any filesystem.
// virtual filesystem like abfs, gitws may provide their own implementation
// of SISO_TAP_COMMAND.
// go/siso-tap describes the interface.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/golang/glog"
	seccomp "github.com/seccomp/libseccomp-golang"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"
)

var ErrNotifStale = errors.New("notification stale")

var (
	runAsSupervised = flag.Bool("supervised", false, "run as supervised")
	showFilter      = flag.Bool("show_filter", false, "show filter")
	showTrace       = flag.Bool("show_trace", false, "show trace")
	restricted      = flag.Bool("restricted", false, "restricted mode")
	tapOutput       = flag.String("tap_output", "", "output of tapping")
)

func printTrace(format string, args ...any) {
	if *showTrace {
		fmt.Printf(format, args...)
	}
}

func newFilter() (*seccomp.ScmpFilter, error) {
	if !*restricted {
		return seccomp.NewFilter(seccomp.ActAllow)
	}
	filter, err := seccomp.NewFilter(seccomp.ActKillProcess)
	if err != nil {
		return nil, err
	}
	for _, sc := range []string{
		"arch_prctl",
		"brk",
		"clone",
		"close",
		"getrandom",
		"ioctl",
		"mmap",
		"mprotect",
		"munmap",
		"prctl",
		"pread64",
		"prlimit64",
		"pwrite64",
		"read",
		"rseq",
		"set_robust_list",
		"set_tid_address",
		"write",
	} {
		call, err := seccomp.GetSyscallFromName(sc)
		if err != nil {
			return nil, fmt.Errorf("syscall by name %q: %w", sc, err)
		}
		err = filter.AddRule(call, seccomp.ActAllow)
		if err != nil {
			return nil, fmt.Errorf("add rule: %w", err)
		}
	}
	return filter, nil

}

func installNotifyFilter() (seccomp.ScmpFd, error) {
	filter, err := newFilter()
	if err != nil {
		return 0, fmt.Errorf("newfilter: %w", err)
	}
	// defer filter.Release()

	// TODO: add more syscalls for file trace?
	for _, sc := range []string{
		"access",
		"chdir",
		"creat",
		"execve",
		"fchdir",
		"fstat",
		"fstatfs",
		"getdents64",
		"getxattr",
		"mkdir",
		"newfstatat",
		"open",
		"openat",
		"rename",
		"rmdir",
		"statfs",
		"statx",

		// strace -trace=%file
		"setxattr",
		"lsetxattr",
		"lgetxattr",
		"listxattr",
		"llistxattr",
		"removexattr",
		"lremovexattr",
		"getcwd",
		"inotify_add_watch",
		"mknodat",
		"mkdirat",
		"unlink",
		"unlinkat",
		"symlinkat",
		"linkat",
		"renameat",
		"umount2",
		"mount",
		"pivot_root",
		"truncate",
		"faccessat",
		"faccessat2",
		"chroot",
		"fchmodat",
		"fchownat",
		"quotactl",
		"readlinkat",
		"utimensat",
		"swapon",
		"swapoff",
		"fanotify_mark",
		"name_to_handle_at",
		"renameat2",
		"execveat",
	} {
		call, err := seccomp.GetSyscallFromName(sc)
		if err != nil {
			return 0, fmt.Errorf("syscall by name %q: %w", sc, err)
		}
		err = filter.AddRule(call, seccomp.ActNotify)
		if err != nil {
			return 0, fmt.Errorf("add rule: %w", err)
		}
	}
	if *showFilter {
		filter.ExportPFC(os.Stdout)
	}
	err = filter.Load()
	if err != nil {
		return 0, fmt.Errorf("load: %w", err)
	}
	fd, err := filter.GetNotifFd()
	if err != nil {
		return 0, fmt.Errorf("notifFd: %w", err)
	}
	return fd, nil
}

func sendfd(w *os.File, fd seccomp.ScmpFd) error {
	nconn, err := net.FileConn(w)
	if err != nil {
		return fmt.Errorf("fileconn: %w", err)
	}
	defer nconn.Close()
	w.Close()
	conn, ok := nconn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("not unix conn? %T", nconn)
	}
	oob := unix.UnixRights(int(fd))
	_, _, err = conn.WriteMsgUnix(nil, oob, nil)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	unix.Close(int(fd))
	return nil
}

func recvfd(r *os.File) (seccomp.ScmpFd, error) {
	if glog.V(1) {
		glog.Infof("accepting...")
	}
	nconn, err := net.FileConn(r)
	if err != nil {
		return 0, fmt.Errorf("fileconn: %w", err)
	}
	defer nconn.Close()
	conn, ok := nconn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("not unix conn? %T", nconn)
	}
	msg := make([]byte, 1024)
	oob := make([]byte, 1024)
	_, oobn, _, _, err := conn.ReadMsgUnix(msg, oob)
	if err != nil {
		return 0, fmt.Errorf("read: %w", err)
	}
	scms, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return 0, fmt.Errorf("parse control: %w", err)
	}
	for _, scm := range scms {
		fds, err := unix.ParseUnixRights(&scm)
		if err != nil {
			continue
		}
		return seccomp.ScmpFd(fds[0]), nil
	}
	return 0, fmt.Errorf("no notify fd")
}

func startTarget() (*exec.Cmd, seccomp.ScmpFd, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("socketpair: %w", err)
	}
	rfd := os.NewFile(uintptr(fds[0]), "fdpassing.r")
	defer rfd.Close()
	wfd := os.NewFile(uintptr(fds[1]), "fdpassing.w")

	args := []string{
		"--supervised",
		fmt.Sprintf("--show_filter=%t", *showFilter),
	}
	cmd := exec.Command(os.Args[0], append(args, flag.Args()...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{wfd}
	err = cmd.Start()
	wfd.Close()
	if err != nil {
		return nil, 0, err
	}
	fd, err := recvfd(rfd)
	if err != nil {
		return nil, 0, err
	}
	return cmd, fd, nil
}

func supervised(w *os.File) {
	err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prctl SET_NEW_PRIVS: %v\n", err)
		os.Exit(1)
	}
	binpath, err := exec.LookPath(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "lookpath(%q): %v\n", flag.Arg(0), err)
		os.Exit(1)
	}
	fd, err := installNotifyFilter()
	if err != nil {
		fmt.Fprintf(os.Stderr, "install seccomp: %v\n", err)
		os.Exit(1)
	}
	// glog.Infof("sending %d...", fd)
	err = sendfd(w, fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sendfd: %v\n", err)
		os.Exit(1)
	}
	// glog.Infof("sendfd %d; start %q", fd, flag.Args())
	err = unix.Exec(binpath, flag.Args(), os.Environ())
	if err != nil {
		// fmt.Fprintf(os.Stderr, "exec: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

const AT_FDCWD = -100

type fileDesc int32

func (f fileDesc) String() string {
	if f == AT_FDCWD {
		return "AT_FDCWD"
	}
	return fmt.Sprintf("%d", int32(f))
}

func strInTarget(pid uint32, ptr uint64) (string, error) {
	buf := make([]byte, 8192)
	lv := []unix.Iovec{
		{
			Base: &buf[0],
			Len:  uint64(len(buf)),
		},
	}
	rv := []unix.RemoteIovec{
		{
			Base: uintptr(ptr),
			Len:  len(buf),
		},
	}
	n, err := unix.ProcessVMReadv(int(pid), lv, rv, 0)
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			return "", fmt.Errorf("%w\nneed: sudo sysctl -w kernel.yama.ptrace_scope=1", err)
		}
		return "", err
	}
	buf = buf[:n]
	i := bytes.IndexByte(buf, 0)
	if i >= 0 {
		buf = buf[:i]
	}
	return string(buf), nil
}

type fop int

const (
	readOp fop = iota
	writeOp
	deleteOp
)

type supervisor struct {
	fops map[string]fop
	cwds map[uint32]string
}

func (s *supervisor) invalidateCwd() {
	// Threads in a thread group share the current working directory.
	// Since chdir/fchdir is rare, clearing all cached cwds ensures
	// any thread will re-read its cwd on its next relative path operation.
	clear(s.cwds)
}

func (s *supervisor) cwd(pid uint32) (string, error) {
	if cwd, ok := s.cwds[pid]; ok {
		return cwd, nil
	}
	cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		delete(s.cwds, pid)
		return "", fmt.Errorf("readlink cwd of %d: %w", pid, err)
	}
	s.cwds[pid] = cwd
	return cwd, nil
}

func (s *supervisor) absname(pid uint32, atfd fileDesc, fname string) (string, error) {
	if filepath.IsAbs(fname) {
		return fname, nil
	}
	if atfd == AT_FDCWD {
		cwd, err := s.cwd(pid)
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, fname), nil
	}
	dir, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", pid, atfd))
	if err != nil {
		return "", fmt.Errorf("readlink fd %d of %d: %w", atfd, pid, err)
	}
	return filepath.Join(dir, fname), nil
}

func (s *supervisor) tapData() map[string][]string {
	m := make(map[string][]string)
	var reads, writes, deletes []string
	for k, v := range s.fops {
		switch v {
		case readOp:
			reads = append(reads, k)
		case writeOp:
			writes = append(writes, k)
		case deleteOp:
			deletes = append(deletes, k)
		}
	}
	sort.Strings(reads)
	if len(reads) > 0 {
		m["reads"] = reads
	}
	sort.Strings(writes)
	if len(writes) > 0 {
		m["writes"] = writes
	}
	sort.Strings(deletes)
	if len(deletes) > 0 {
		m["deletes"] = deletes
	}
	return m
}

func (s *supervisor) Run(fd seccomp.ScmpFd, done <-chan struct{}) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("panic: %v", r)
		}
	}()
	pfds := []unix.PollFd{
		{
			Fd:     int32(fd),
			Events: unix.POLLIN,
		},
	}
	var fdHup bool
	for {
		select {
		case <-done:
			if !fdHup {
				glog.Infof("supervisor loop done: cmd finished")
			}
			return nil
		default:
		}

		n, err := unix.Poll(pfds, 50)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return fmt.Errorf("poll: %w", err)
		}

		if n == 0 {
			continue
		}
		fdHup = pfds[0].Revents&unix.POLLHUP != 0
		fdErr := pfds[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0
		hasNotif := pfds[0].Revents&unix.POLLIN != 0

		if !hasNotif {
			if fdHup || fdErr {
				if glog.V(1) {
					glog.Infof("poll wait: fdHup=%t fdErr=%t", fdHup, fdErr)
				}
			}
			// keep poll until target process finished, i.e. <-done.
			continue
		}

		req, err := seccomp.NotifReceive(fd)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.ENOENT) {
				glog.Warningf("receive: %v (retrying)", err)
				select {
				case <-done:
					return nil
				default:
				}
				continue
			}
			return fmt.Errorf("receive: %w", err)
		}
		if glog.V(1) {
			glog.Infof("notification %d PID=%d arch=%q", req.ID, req.Pid, req.Data.Arch)
		}
		resp, err := s.handleNotif(fd, req)
		if err != nil {
			if errors.Is(err, ErrNotifStale) {
				glog.Warningf("notification %d stale: %v", req.ID, err)
				continue
			}
			return fmt.Errorf("handle: %w", err)
		}
		if resp != nil {
			err = seccomp.NotifRespond(fd, resp)
			if err != nil {
				if errors.Is(err, unix.ENOENT) {
					glog.Warningf("reply stale notification %d: %v", req.ID, err)
					continue
				}
				return fmt.Errorf("reply: %w", err)
			}
		}
		if glog.V(1) {
			glog.Infof("continue")
		}
	}
}

func (s *supervisor) handleNotif(fd seccomp.ScmpFd, req *seccomp.ScmpNotifReq) (resp *seccomp.ScmpNotifResp, retErr error) {
	defer func() {
		switch r := recover(); r := r.(type) {
		case unix.Errno:
			// fmt.Printf("panic errno %v\n", r)
			resp = &seccomp.ScmpNotifResp{
				ID:    req.ID,
				Error: int32(r),
			}
			retErr = nil
		case error:
			if errors.Is(r, ErrNotifStale) {
				delete(s.cwds, req.Pid)
				resp = nil
				retErr = ErrNotifStale
				return
			}
			if err := seccomp.NotifIDValid(fd, req.ID); err != nil && errors.Is(err, unix.ENOENT) {
				delete(s.cwds, req.Pid)
				resp = nil
				retErr = ErrNotifStale
				return
			}
			glog.Errorf("unexpected error handling notification %d: %v", req.ID, r)
			// Target process is still alive and waiting; do not drop response.
			resp = &seccomp.ScmpNotifResp{
				ID:    req.ID,
				Flags: seccomp.NotifRespFlagContinue,
			}
			retErr = nil
		case nil:
		default:
			glog.Errorf("unexpected panic handling notification %d: %v", req.ID, r)
			if err := seccomp.NotifIDValid(fd, req.ID); err != nil && errors.Is(err, unix.ENOENT) {
				delete(s.cwds, req.Pid)
				resp = nil
				retErr = ErrNotifStale
				return
			}
			resp = &seccomp.ScmpNotifResp{
				ID:    req.ID,
				Flags: seccomp.NotifRespFlagContinue,
			}
			retErr = nil
		}
	}()
	strInReq := func(i int) string {
		ptr := req.Data.Args[i]
		if ptr == 0 {
			panic(unix.EFAULT)
		}
		s, err := strInTarget(req.Pid, ptr)
		if err != nil {
			if errors.Is(err, syscall.EFAULT) {
				panic(unix.EFAULT)
			}
			if vErr := seccomp.NotifIDValid(fd, req.ID); vErr != nil && errors.Is(vErr, unix.ENOENT) {
				panic(ErrNotifStale)
			}
			sysName, errName := req.Data.Syscall.GetName()
			if errName != nil {
				sysName = fmt.Sprintf("syscall_%d", req.Data.Syscall)
			}
			glog.Errorf("[%s] arg[%d] strInTarget(%d, 0x%x): %v", sysName, i, req.Pid, ptr, err)
			panic(unix.EINVAL)
		}
		err = seccomp.NotifIDValid(fd, req.ID)
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				panic(ErrNotifStale)
			}
			panic(unix.EINVAL)
		}
		return s
	}
	syscallName, err := req.Data.Syscall.GetName()
	if err != nil {
		glog.Errorf("syscall name for %d: %v", req.Data.Syscall, err)
	}
	resp = &seccomp.ScmpNotifResp{
		ID:    req.ID,
		Error: 0,
		Val:   0,
		Flags: seccomp.NotifRespFlagContinue,
	}
	absname := func(atfd fileDesc, fname string) (string, bool) {
		abs, err := s.absname(req.Pid, atfd, fname)
		if err != nil {
			if vErr := seccomp.NotifIDValid(fd, req.ID); vErr != nil && errors.Is(vErr, unix.ENOENT) {
				panic(ErrNotifStale)
			}
			glog.Warningf("[%s] absname(%d, %v, %q): %v", syscallName, req.Pid, atfd, fname, err)
			return "", false
		}
		return abs, true
	}
	markAsRead := func(fname string) {
		switch s.fops[fname] {
		case writeOp:
			return
		}
		s.fops[fname] = readOp
	}
	markAsWrite := func(fname string) {
		s.fops[fname] = writeOp
	}
	markAsDelete := func(fname string) {
		s.fops[fname] = deleteOp
	}

	switch syscallName {
	case "access":
		fname := strInReq(0)
		if abs, ok := absname(AT_FDCWD, fname); ok {
			markAsRead(abs)
		}
		printTrace(" access(%q, %s)\n", fname, os.FileMode(req.Data.Args[1]))
	case "chdir":
		dname := strInReq(0)
		if abs, ok := absname(AT_FDCWD, dname); ok {
			markAsRead(abs)
		}
		s.invalidateCwd()
		printTrace(" chdir(%q)\n", dname)
	case "fchdir":
		atfd := fileDesc(req.Data.Args[0])
		dir, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", req.Pid, atfd))
		if err != nil {
			if vErr := seccomp.NotifIDValid(fd, req.ID); vErr != nil && errors.Is(vErr, unix.ENOENT) {
				panic(ErrNotifStale)
			}
			glog.Warningf("fchdir: readlink(%d, %v): %v", req.Pid, atfd, err)
		} else {
			markAsRead(dir)
		}
		s.invalidateCwd()
		printTrace(" fchdir(%v)\n", atfd)
	case "chroot":
		dname := strInReq(0)
		if abs, ok := absname(AT_FDCWD, dname); ok {
			markAsRead(abs)
		}
		s.invalidateCwd()
		printTrace(" chroot(%q)\n", dname)
	case "pivot_root":
		newroot := strInReq(0)
		oldroot := strInReq(1)
		if absNew, ok := absname(AT_FDCWD, newroot); ok {
			markAsRead(absNew)
		}
		if absOld, ok := absname(AT_FDCWD, oldroot); ok {
			markAsRead(absOld)
		}
		s.invalidateCwd()
		printTrace(" pivot_root(%q, %q)\n", newroot, oldroot)
	case "creat":
		fname := strInReq(0)
		if abs, ok := absname(AT_FDCWD, fname); ok {
			markAsWrite(abs)
		}
		printTrace(" creat(%q, %s)\n", fname, os.FileMode(req.Data.Args[1]))
	case "execve":
		fname := strInReq(0)
		if abs, ok := absname(AT_FDCWD, fname); ok {
			markAsRead(abs)
		}
		printTrace(" execve(%q, %v, %v)\n", fname, req.Data.Args[1], req.Data.Args[2])
	case "execveat":
		atfd := fileDesc(req.Data.Args[0])
		pathname := strInReq(1)
		if abs, ok := absname(atfd, pathname); ok {
			markAsRead(abs)
		}
		printTrace(" execveat(%v, %q, %v, %v, 0x%x)\n", atfd, pathname, req.Data.Args[2], req.Data.Args[3], req.Data.Args[4])
	case "faccessat", "faccessat2":
		atfd := fileDesc(req.Data.Args[0])
		pathname := strInReq(1)
		if abs, ok := absname(atfd, pathname); ok {
			markAsRead(abs)
		}
		printTrace(" %s(%v, %q, %v, 0x%x)\n", syscallName, atfd, pathname, os.FileMode(req.Data.Args[2]), req.Data.Args[3])
	case "fstat":
		printTrace(" fstat(%d, 0x%x)\n", fileDesc(req.Data.Args[0]), uintptr(req.Data.Args[1]))
	case "fstatfs":
		printTrace(" fstatfs(%d, 0x%x)\n", fileDesc(req.Data.Args[0]), uintptr(req.Data.Args[1]))
	case "mkdir":
		dname := strInReq(0)
		if abs, ok := absname(AT_FDCWD, dname); ok {
			markAsWrite(abs)
		}
		printTrace(" mkdir(%q , 0o%o)\n", dname, req.Data.Args[1])
	case "mkdirat":
		atfd := fileDesc(req.Data.Args[0])
		fname := strInReq(1)
		if abs, ok := absname(atfd, fname); ok {
			markAsWrite(abs)
		}
		printTrace(" mkdirat(%d, %q, 0o%o)\n", atfd, fname, req.Data.Args[2])
	case "rmdir":
		dname := strInReq(0)
		if abs, ok := absname(AT_FDCWD, dname); ok {
			markAsDelete(abs)
		}
		printTrace(" rmdir(%q)\n", dname)
	case "newfstatat":
		atfd := fileDesc(req.Data.Args[0])
		fname := strInReq(1)
		if abs, ok := absname(atfd, fname); ok {
			markAsRead(abs)
		}
		printTrace(" newfstatat(%v, %q, 0x%x, 0x%x)\n", atfd, fname, req.Data.Args[2], req.Data.Args[3])
	case "open":
		pathname := strInReq(0)
		if absname, ok := absname(AT_FDCWD, pathname); ok {
			if (req.Data.Args[1] & uint64(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_APPEND|os.O_TRUNC)) != 0 {
				markAsWrite(absname)
			} else {
				markAsRead(absname)
			}
		}
		printTrace(" open(%q, 0x%x, %s)\n", pathname, req.Data.Args[1], os.FileMode(req.Data.Args[2]))
	case "openat":
		atfd := fileDesc(req.Data.Args[0])
		pathname := strInReq(1)
		if absname, ok := absname(atfd, pathname); ok {
			if (req.Data.Args[2] & uint64(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_APPEND|os.O_TRUNC)) != 0 {
				markAsWrite(absname)
			} else {
				markAsRead(absname)
			}
		}
		printTrace(" openat(%v, %q, 0x%x, %s)\n", atfd, pathname, req.Data.Args[2], os.FileMode(req.Data.Args[3]))
	case "readlinkat":
		atfd := fileDesc(req.Data.Args[0])
		pathname := strInReq(1)
		if abs, ok := absname(atfd, pathname); ok {
			markAsRead(abs)
		}
		printTrace(" readlinkat(%s, %q, 0x%x, %d)\n", atfd, pathname, req.Data.Args[2], req.Data.Args[3])
	case "rename":
		oldpath := strInReq(0)
		newpath := strInReq(1)
		if oldabs, ok := absname(AT_FDCWD, oldpath); ok {
			markAsDelete(oldabs)
		}
		if newabs, ok := absname(AT_FDCWD, newpath); ok {
			markAsWrite(newabs)
		}
		printTrace(" rename(%q, %q)\n", oldpath, newpath)
	case "renameat", "renameat2":
		oldatfd := fileDesc(req.Data.Args[0])
		oldpath := strInReq(1)
		newatfd := fileDesc(req.Data.Args[2])
		newpath := strInReq(3)
		if oldabs, ok := absname(oldatfd, oldpath); ok {
			markAsDelete(oldabs)
		}
		if newabs, ok := absname(newatfd, newpath); ok {
			markAsWrite(newabs)
		}
		printTrace(" %s(%v, %q, %v, %q)\n", syscallName, oldatfd, oldpath, newatfd, newpath)
	case "stat":
		pathname := strInReq(0)
		if abs, ok := absname(AT_FDCWD, pathname); ok {
			markAsRead(abs)
		}
		printTrace(" stat(%q, 0x%x)\n", pathname, req.Data.Args[1])
	case "statfs":
		pathname := strInReq(0)
		if abs, ok := absname(AT_FDCWD, pathname); ok {
			markAsRead(abs)
		}
		printTrace(" statfs(%q, 0x%x)\n", pathname, req.Data.Args[1])
	case "statx":
		atfd := fileDesc(req.Data.Args[0])
		pathname := strInReq(1)
		if abs, ok := absname(atfd, pathname); ok {
			markAsRead(abs)
		}
		printTrace(" statx(%v, %q, 0x%x, 0x%x, 0x%x)\n", atfd, pathname, req.Data.Args[2], req.Data.Args[3], req.Data.Args[4])
	case "unlink":
		pathname := strInReq(0)
		if abs, ok := absname(AT_FDCWD, pathname); ok {
			markAsDelete(abs)
		}
		printTrace(" unlink(%q)\n", pathname)
	case "unlinkat":
		atfd := fileDesc(req.Data.Args[0])
		pathname := strInReq(1)
		if abs, ok := absname(atfd, pathname); ok {
			markAsDelete(abs)
		}
		printTrace(" unlinkat(%v, %q)\n", atfd, pathname)
	case "utimensat":
		atfd := fileDesc(req.Data.Args[0])
		var pathname string
		if req.Data.Args[1] != 0 {
			pathname = strInReq(1)
		}
		if abs, ok := absname(atfd, pathname); ok {
			markAsWrite(abs)
		}
		printTrace(" utimensat(%v, %q, 0x%x, 0x%x)\n", atfd, pathname, req.Data.Args[2], req.Data.Args[3])
	case "getdents64":
		printTrace(" getdents64(%d, 0x%x, %d)\n", int32(req.Data.Args[0]), req.Data.Args[1], req.Data.Args[2])
	case "getxattr":
		pathname := strInReq(0)
		if abs, ok := absname(AT_FDCWD, pathname); ok {
			markAsRead(abs)
		}
		name := strInReq(1)
		printTrace(" getxattr(%q, %q, 0x%x, %d)\n", pathname, name, req.Data.Args[2], req.Data.Args[3])
	default:
		printTrace(" %s(", syscallName)
		if glog.V(1) {
			glog.Infof(" syscall=%d %q: %v", req.Data.Syscall, syscallName, err)
		}
		for i, arg := range req.Data.Args {
			printTrace("0x%x", arg)
			if i != len(req.Data.Args)-1 {
				printTrace(", ")
			}
			if glog.V(1) {
				glog.Infof(" args[%d]=%d", i, arg)
			}
		}
		printTrace(")\n")
	}
	err = seccomp.NotifIDValid(fd, req.ID)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			panic(ErrNotifStale)
		}
		glog.Errorf("failed to check id: %v", err)
		panic(unix.EINVAL)
	}
	return resp, nil
}

func main() {
	flag.Parse()
	if *runAsSupervised {
		supervised(os.NewFile(3, "fdpassing"))
		fmt.Printf("supervied exit\n")
		return
	}
	defer glog.Flush()
	cmd, fd, err := startTarget()
	if err != nil {
		glog.Fatalf("target: %v", err)
	}
	if glog.V(1) {
		glog.Infof("notify fd=%d\n", fd)
	}

	done := make(chan struct{})
	s := &supervisor{
		fops: make(map[string]fop),
		cwds: make(map[uint32]string),
	}
	var eg errgroup.Group
	var cmdErr error
	eg.Go(func() error {
		defer unix.Close(int(fd))
		return s.Run(fd, done)
	})
	eg.Go(func() error {
		defer close(done)
		cmdErr = cmd.Wait()
		return cmdErr
	})
	err = eg.Wait()
	if err != nil {
		glog.Infof("done: %v", err)
	}
	if *tapOutput != "" {
		buf, berr := json.MarshalIndent(s.tapData(), "", " ")
		if berr != nil {
			glog.Warningf("marshal: %v", berr)
		}
		berr = os.WriteFile(*tapOutput, buf, 0644)
		if berr != nil {
			glog.Warningf("save %q: %v", *tapOutput, berr)
		}
	}
	glog.Flush()
	// fmt.Printf("cmdErr: %v\n", cmdErr)
	if cmdErr != nil {
		if cmd.ProcessState != nil {
			code := cmd.ProcessState.ExitCode()
			if code != 0 {
				os.Exit(code)
			}
		}
		os.Exit(1)
	}
	if err != nil {
		os.Exit(1)
	}
}
