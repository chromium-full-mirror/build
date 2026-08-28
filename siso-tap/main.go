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
		"fstat",
		"fstatfs",
		"getdents64",
		"getxattr",
		"mkdir",
		"newfstatat",
		"open",
		"openat",
		"rename",
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
	glog.Infof("accepting...")
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
	defer wfd.Close()

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
}

func (s *supervisor) absname(pid uint32, atfd fileDesc, fname string) string {
	if filepath.IsAbs(fname) {
		return fname
	}
	// TODO: cache?
	if atfd == AT_FDCWD {
		cwd, err := filepath.EvalSymlinks(fmt.Sprintf("/proc/%d/cwd", pid))
		if err != nil {
			panic(fmt.Errorf("failed to get cwd of %d: %w", pid, err))
		}
		return filepath.Join(cwd, fname)
	}
	dir, err := filepath.EvalSymlinks(fmt.Sprintf("/proc/%d/fd/%s", pid, atfd))
	if err != nil {
		panic(fmt.Errorf("failed to get fd %d of %d: %w", atfd, pid, err))
	}
	return filepath.Join(dir, fname)
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

func (s *supervisor) Run(fd seccomp.ScmpFd) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("panic: %v", r)
		}
	}()
	for {
		req, err := seccomp.NotifReceive(fd)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.ENOENT) {
				glog.Warningf("receive: %v (retrying)", err)
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
			// fmt.Printf("panic error %v\n", r)
			resp = nil
			retErr = r
		case nil:
		default:
			// fmt.Printf("panic? %v (%T)\n", r, r)
			resp = nil
			retErr = fmt.Errorf("panic: %v", r)
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
		markAsRead(s.absname(req.Pid, AT_FDCWD, fname))
		printTrace(" access(%q, %s)\n", fname, os.FileMode(req.Data.Args[1]))
	case "chdir":
		dname := strInReq(0)
		markAsRead(s.absname(req.Pid, AT_FDCWD, dname))
		printTrace(" chdir(%q)\n", dname)
	case "creat":
		fname := strInReq(0)
		markAsWrite(s.absname(req.Pid, AT_FDCWD, fname))
		printTrace(" creat(%q, %s)\n", fname, os.FileMode(req.Data.Args[1]))
	case "execve":
		fname := strInReq(0)
		markAsRead(s.absname(req.Pid, AT_FDCWD, fname))
		printTrace(" execve(%q, %v, %v)\n", fname, req.Data.Args[1], req.Data.Args[2])
	case "faccessat", "faccessat2":
		atfd := fileDesc(req.Data.Args[0])
		pathname := strInReq(1)
		markAsRead(s.absname(req.Pid, atfd, pathname))
		printTrace(" %s(%v, %q, %v, 0x%x)\n", syscallName, atfd, pathname, os.FileMode(req.Data.Args[2]), req.Data.Args[3])
	case "fstat":
		printTrace(" fstat(%d, 0x%x)\n", fileDesc(req.Data.Args[0]), uintptr(req.Data.Args[1]))
	case "fstatfs":
		printTrace(" fstatfs(%d, 0x%x)\n", fileDesc(req.Data.Args[0]), uintptr(req.Data.Args[1]))
	case "mkdir":
		dname := strInReq(0)
		markAsWrite(s.absname(req.Pid, AT_FDCWD, dname))
		printTrace(" mkdir(%q , 0o%o)\n", dname, req.Data.Args[1])
	case "mkdirat":
		atfd := fileDesc(req.Data.Args[0])
		fname := strInReq(1)
		markAsWrite(s.absname(req.Pid, atfd, fname))
		printTrace(" mkdirat(%d, %q, 0o%o)\n", atfd, fname, req.Data.Args[2])
	case "newfstatat":
		atfd := fileDesc(req.Data.Args[0])
		fname := strInReq(1)
		markAsRead(s.absname(req.Pid, atfd, fname))
		printTrace(" newfstatat(%v, %q, 0x%x, 0x%x)\n", atfd, fname, req.Data.Args[2], req.Data.Args[3])
	case "open":
		pathname := strInReq(0)
		absname := s.absname(req.Pid, AT_FDCWD, pathname)
		if (req.Data.Args[1] & uint64(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_APPEND|os.O_TRUNC)) != 0 {
			markAsWrite(absname)
		} else {
			markAsRead(absname)
		}
		printTrace(" open(%q, 0x%x, %s)\n", pathname, req.Data.Args[1], os.FileMode(req.Data.Args[2]))
	case "openat":
		atfd := fileDesc(req.Data.Args[0])
		pathname := strInReq(1)
		absname := s.absname(req.Pid, atfd, pathname)
		if (req.Data.Args[2] & uint64(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_APPEND|os.O_TRUNC)) != 0 {
			markAsWrite(absname)
		} else {
			markAsRead(absname)
		}
		printTrace(" openat(%v, %q, 0x%x, %s)\n", atfd, pathname, req.Data.Args[2], os.FileMode(req.Data.Args[3]))
		// TODO: use /proc/$req.Pid/cwd's link for AT_FDCWD (cached per PID?)

	case "readlinkat":
		atfd := fileDesc(req.Data.Args[0])
		pathname := strInReq(1)
		markAsRead(s.absname(req.Pid, atfd, pathname))
		printTrace(" readlinkat(%s, %q, 0x%x, %d)\n", atfd, pathname, req.Data.Args[2], req.Data.Args[3])
	case "rename":
		oldpath := strInReq(0)
		newpath := strInReq(1)
		markAsDelete(s.absname(req.Pid, AT_FDCWD, oldpath))
		markAsWrite(s.absname(req.Pid, AT_FDCWD, newpath))
		printTrace(" rename(%q, %q)\n", oldpath, newpath)
	case "renameat":
		oldatfd := fileDesc(req.Data.Args[0])
		oldpath := strInReq(1)
		newatfd := fileDesc(req.Data.Args[2])
		newpath := strInReq(3)
		markAsDelete(s.absname(req.Pid, oldatfd, oldpath))
		markAsWrite(s.absname(req.Pid, newatfd, newpath))
		printTrace(" renameat(%v, %q, %v, %q)\n", oldatfd, oldpath, newatfd, newpath)
	case "stat":
		pathname := strInReq(0)
		markAsRead(s.absname(req.Pid, AT_FDCWD, pathname))
		printTrace(" stat(%q, 0x%x)\n", pathname, req.Data.Args[1])
	case "statfs":
		pathname := strInReq(0)
		markAsRead(s.absname(req.Pid, AT_FDCWD, pathname))
		printTrace(" statfs(%q, 0x%x)\n", pathname, req.Data.Args[1])
	case "statx":
		atfd := fileDesc(req.Data.Args[0])
		pathname := strInReq(1)
		markAsRead(s.absname(req.Pid, atfd, pathname))
		printTrace(" statx(%v, %q, 0x%x, 0x%x, 0x%x)\n", atfd, pathname, req.Data.Args[2], req.Data.Args[3], req.Data.Args[4])
	case "unlink":
		pathname := strInReq(0)
		markAsDelete(s.absname(req.Pid, AT_FDCWD, pathname))
		printTrace(" unlink(%q)\n", pathname)
	case "unlinkat":
		atfd := fileDesc(req.Data.Args[0])
		pathname := strInReq(1)
		markAsDelete(s.absname(req.Pid, atfd, pathname))
		printTrace(" unlinkat(%v, %q)\n", atfd, pathname)
	case "utimensat":
		atfd := fileDesc(req.Data.Args[0])
		var pathname string
		if req.Data.Args[1] != 0 {
			pathname = strInReq(1)
		}
		markAsWrite(s.absname(req.Pid, atfd, pathname))
		printTrace(" utimensat(%v, %q, 0x%x, 0x%x)\n", atfd, pathname, req.Data.Args[2], req.Data.Args[3])
	case "getdents64":
		printTrace(" getdents64(%d, 0x%x, %d)\n", int32(req.Data.Args[0]), req.Data.Args[1], req.Data.Args[2])
	case "getxattr":
		pathname := strInReq(0)
		markAsRead(s.absname(req.Pid, AT_FDCWD, pathname))
		name := strInReq(1)
		printTrace(" getxattr(%q, %q, 0x%x, %d)\n", pathname, name, req.Data.Args[2], req.Data.Args[3])
	default:
		printTrace(" %s(", syscallName)
		glog.Infof(" syscall=%d %q: %v", req.Data.Syscall, syscallName, err)
		for i, arg := range req.Data.Args {
			printTrace("0x%x", arg)
			if i != len(req.Data.Args)-1 {
				printTrace(", ")
			}
			glog.Infof(" args[%d]=%d", i, arg)
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
	glog.Infof("notify fd=%d\n", fd)

	s := &supervisor{fops: make(map[string]fop)}
	var eg errgroup.Group
	var cmdErr error
	eg.Go(func() error {
		defer unix.Close(int(fd))
		return s.Run(fd)
	})
	eg.Go(func() error {
		cmdErr = cmd.Wait()
		unix.Close(int(fd))
		return cmdErr
	})
	err = eg.Wait()
	glog.Infof("done: %v", err)
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
}
