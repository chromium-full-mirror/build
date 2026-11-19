#!/usr/bin/env python3
# Copyright 2026 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.
"""strace-based implementation of the SISO_TAP_COMMAND contract.

Siso runs local steps through the command named by the SISO_TAP_COMMAND
environment variable (see siso/execute/localexec/tap.go) as

  tapcmd.py --tap_output <file> -- <cmd> <args...>

The wrapper runs the command under strace, writes a TapResult JSON object
(see siso/execute/proto/tap.proto) to <file>, and exits like the command.
The result lists the absolute paths that the command read, wrote and
deleted. Siso adds them to the step's inputs and outputs, so that it can
upload the local result to the remote cache, e.g. for two-phase caching.

Files that the command writes are outputs only, even if it read them.
Files that it only stats count as read. For each directory that it
lists, the result has the entries from its first listing, except the
ones that the command created, changed or deleted before, which it
reports as ignored names.

If the trace can't account for every file access, e.g. because the
command used io_uring or chroot, the result has an error and Siso
doesn't cache the step. The trace misses files that processes outside
the command's process tree access for it, e.g. a compiler server, and
Siso can't record that a step depends on a file not existing.

Linux only, needs strace. Example:

  SISO_EXPERIMENTS=two-phase-caching \\
  SISO_TAP_COMMAND=/abs/path/to/tapcmd.py \\
  siso ninja -C out/dir -re_cache_enable_write <target>
"""

import json
import os
import re
import signal
import subprocess
import sys
import tempfile

SYSCALLS = """
    open openat openat2 creat execve execveat readlink readlinkat
    stat lstat newfstatat statx access faccessat faccessat2
    getdents getdents64
    mkdir mkdirat mknod mknodat link linkat symlink symlinkat
    rename renameat renameat2 unlink unlinkat rmdir
    truncate chmod fchmodat fchmodat2 utime utimes utimensat futimesat
    chdir fchdir clone clone3 fork vfork
    io_uring_setup open_by_handle_at chroot pivot_root
""".split()
CLONES = ("clone", "clone3", "fork", "vfork")
DIRENT_TYPES = {
  "DT_REG": None,
  "DT_DIR": "DIR",
  "DT_LNK": "SYMLINK",
  "DT_FIFO": "OTHER",
  "DT_SOCK": "OTHER",
  "DT_CHR": "OTHER",
  "DT_BLK": "OTHER",
}

STRACE = [
  "strace",
  "--follow-forks",
  "--seccomp-bpf",
  "-qq",
  "--signal=none",
  # strace prints a successful syscall only when it returns, so lines
  # are never split into <unfinished ...> and <... resumed>.
  "--status=successful",
  "--decode-fds=path",
  "--strings-in-hex=all",
  "--abbrev=!?getdents,?getdents64",
  # "?" ignores syscalls that the architecture doesn't have.
  "--trace=" + ",".join("?" + s for s in SYSCALLS),
]

LINE_RE = re.compile(r"(\d+) +(\w+)\((.*)\) += (-?\d+)")
SYSCALL_START_RE = re.compile(r"\d+ +\w+\(")
HEX_RE = re.compile(r"(?:\\x[0-9a-f]{2})*")
FD_RE = re.compile(r"(AT_FDCWD|\d+)(?:<(.*)>)?")
DIRENT_RE = re.compile(r'd_type=(\w+), d_name="((?:\\x[0-9a-f]{2})*)"')


class TraceError(Exception):
  pass


def split_args(s: str) -> list[str]:
  args, start, depth, i = [], 0, 0, 0
  while i < len(s):
    c = s[i]
    if c == '"':
      i = s.index('"', i + 1)
    elif s.startswith("<\\x", i):
      i = s.index(">", i)
    elif c in "([{":
      depth += 1
    elif c in ")]}":
      depth -= 1
    elif c == "," and depth == 0:
      args.append(s[start:i].strip())
      start = i + 1
    i += 1
  args.append(s[start:].strip())
  return args


def unhex(s: str) -> str:
  if not HEX_RE.fullmatch(s):
    raise TraceError(f"unexpected string {s!r}")
  try:
    return bytes.fromhex(s.replace("\\x", "")).decode()
  except UnicodeDecodeError:
    raise TraceError(f"non-UTF-8 path {s}") from None


class Fs:
  """The cwd of a process, shared by the threads that share it."""

  def __init__(self, cwd: str):
    self.cwd = cwd


class Listing:
  """A directory that the command listed."""

  def __init__(self, ignored: set[str]):
    self.ignored = ignored
    self.entries = {}


class Tap:
  def __init__(self, cwd: str):
    self.cwd = cwd
    self.reads, self.writes, self.deletes = set(), set(), set()
    self.renamed = set()
    self.parents = {}
    self.fs_of = {}
    # Names of the entries that the command changed, by directory with
    # symlinks resolved.
    self.changed = {}
    # Directories that the command created, with symlinks resolved.
    self.created = set()
    # Listings by directory. None if the listing doesn't matter.
    self.listings = {}
    # Directories whose first listing a (pid, fd) is reading.
    self.listing_fds = {}
    self.realdirs = {}

  def fs(self, pid: int) -> Fs:
    fs = self.fs_of.get(pid)
    if fs is None:
      if pid in self.parents:
        ppid, shares_fs = self.parents[pid]
        parent = self.fs(ppid)
        fs = parent if shares_fs else Fs(parent.cwd)
      elif not self.fs_of:
        fs = Fs(self.cwd)
      else:
        raise TraceError(f"unknown process {pid}")
      self.fs_of[pid] = fs
    return fs

  def path(self, pid: int, dirfd: str | None, arg: str) -> str:
    if len(arg) < 2 or arg[0] != '"' or arg[-1] != '"':
      raise TraceError(f"unreadable path {arg}")
    path = unhex(arg[1:-1])
    base = None
    if dirfd is not None:
      m = FD_RE.fullmatch(dirfd)
      if not m:
        raise TraceError(f"unexpected dirfd {dirfd}")
      if m.group(2) is not None:
        base = unhex(m.group(2))
        if m.group(1) == "AT_FDCWD":
          self.fs(pid).cwd = base
      elif m.group(1) != "AT_FDCWD":
        raise TraceError(f"no path for fd {dirfd}")
    if base is None:
      base = self.fs(pid).cwd
    return os.path.normpath(os.path.join(base, path))

  def resolve_dir(self, path: str) -> str:
    d = os.path.dirname(path)
    if d not in self.realdirs:
      self.realdirs[d] = os.path.realpath(d)
    return os.path.join(self.realdirs[d], os.path.basename(path))

  def note_change(self, path: str):
    d, name = os.path.split(self.resolve_dir(path))
    self.changed.setdefault(d, set()).add(name)

  def write(self, path: str):
    self.writes.add(path)
    self.note_change(path)

  def delete(self, path: str):
    self.deletes.add(path)
    self.note_change(path)

  def create_dir(self, path: str):
    self.write(path)
    self.created.add(self.resolve_dir(path))

  def open(self, path: str, flags: str):
    flags = set(flags.split("|"))
    if flags & {"O_PATH", "O_TMPFILE", "__O_TMPFILE"}:
      return
    if "O_RDONLY" in flags:
      self.reads.add(path)
    elif flags & {"O_WRONLY", "O_RDWR"}:
      self.write(path)
    else:
      raise TraceError(f"unexpected open flags {flags}")

  def rename(self, old: str, new: str, flags: str):
    if "RENAME_EXCHANGE" in flags:
      self.create_dir(old)
      self.renamed.add(old)
    else:
      self.delete(old)
    self.create_dir(new)
    self.renamed.add(new)

  def list_dir(self, pid: int, dirfd: str, dirents: str, ret: int):
    m = FD_RE.fullmatch(dirfd)
    if not m or m.group(2) is None:
      raise TraceError(f"no path for fd {dirfd}")
    d, key = unhex(m.group(2)), (pid, m.group(1))
    if d not in self.listings:
      ignored = set(self.changed.get(d, ()))
      self.listings[d] = None if d in self.created else Listing(ignored)
      self.listing_fds[key] = d
    if self.listing_fds.get(key) != d:
      return
    if ret == 0:
      del self.listing_fds[key]
    listing = self.listings[d]
    if listing is None:
      return
    found = DIRENT_RE.findall(dirents)
    if ret > 0 and not found:
      raise TraceError(f"unreadable listing of {d}")
    for typ, name in found:
      name = unhex(name)
      if name in (".", "..") or name in listing.ignored:
        continue
      if typ not in DIRENT_TYPES:
        raise TraceError(f"{typ} entry {name} in {d}")
      listing.entries[name] = DIRENT_TYPES[typ]

  def readdirs(self) -> list[dict]:
    result = []
    for d, listing in sorted(self.listings.items()):
      if listing is None:
        continue
      readdir = {"dir": d}
      if listing.entries:
        readdir["entries"] = [
          {"name": name, "type": typ} if typ else {"name": name}
          for name, typ in sorted(listing.entries.items())
        ]
      if listing.ignored:
        readdir["ignored"] = sorted(listing.ignored)
      result.append(readdir)
    return result

  def syscall(self, pid: int, name: str, args: list[str], ret: int):
    def at(dirfd, path):
      return self.path(pid, dirfd, path)

    match name, args:
      case "open", [path, flags, *_]:
        self.open(at(None, path), flags)
      case "openat", [dirfd, path, flags, *_]:
        self.open(at(dirfd, path), flags)
      case "openat2", [dirfd, path, how, *_]:
        self.open(at(dirfd, path), re.search(r"flags=([^,}]+)", how).group(1))
      case "creat", [path, *_]:
        self.write(at(None, path))
      case "execve" | "readlink" | "stat" | "lstat" | "access", [path, *_]:
        self.reads.add(at(None, path))
      case "execveat" | "readlinkat", [dirfd, path, *_]:
        self.reads.add(at(dirfd, path))
      case (
        "newfstatat" | "statx" | "faccessat" | "faccessat2",
        [dirfd, path, *_],
      ):
        if path != '""':
          self.reads.add(at(dirfd, path))
      case "getdents" | "getdents64", [dirfd, dirents, *_]:
        self.list_dir(pid, dirfd, dirents, ret)
      case "mkdir", [path, *_]:
        self.create_dir(at(None, path))
      case "mkdirat", [dirfd, path, *_]:
        self.create_dir(at(dirfd, path))
      case "mknod" | "truncate" | "chmod" | "utime" | "utimes", [path, *_]:
        self.write(at(None, path))
      case "mknodat", [dirfd, path, *_]:
        self.write(at(dirfd, path))
      case (
        "fchmodat" | "fchmodat2" | "utimensat" | "futimesat",
        [dirfd, path, *_],
      ):
        if path != "NULL":
          self.write(at(dirfd, path))
      case "symlink", [_, path]:
        self.write(at(None, path))
      case "symlinkat", [_, dirfd, path]:
        self.write(at(dirfd, path))
      case "link", [old, new]:
        self.reads.add(at(None, old))
        self.write(at(None, new))
      case "linkat", [old_dirfd, old, new_dirfd, new, *_]:
        self.reads.add(at(old_dirfd, old))
        self.write(at(new_dirfd, new))
      case "rename", [old, new]:
        self.rename(at(None, old), at(None, new), "")
      case "renameat", [old_dirfd, old, new_dirfd, new]:
        self.rename(at(old_dirfd, old), at(new_dirfd, new), "")
      case "renameat2", [old_dirfd, old, new_dirfd, new, flags]:
        self.rename(at(old_dirfd, old), at(new_dirfd, new), flags)
      case "unlink" | "rmdir", [path]:
        self.delete(at(None, path))
      case "unlinkat", [dirfd, path, _]:
        self.delete(at(dirfd, path))
      case "chdir", [path]:
        d = at(None, path)
        self.reads.add(d)
        self.fs(pid).cwd = os.path.realpath(d)
      case "fchdir", [fd]:
        m = FD_RE.fullmatch(fd)
        if not m or m.group(2) is None:
          raise TraceError(f"no path for fd {fd}")
        self.fs(pid).cwd = unhex(m.group(2))
      case "io_uring_setup" | "open_by_handle_at" | "chroot" | "pivot_root", _:
        raise TraceError(f"can't trace file accesses after {name}")
      case _:
        raise TraceError(f"unexpected arguments {args}")

  def add_renamed_dir_contents(self):
    for d in self.renamed:
      if os.path.isdir(d) and not os.path.islink(d):
        for root, dirs, files in os.walk(d):
          self.writes.update(os.path.join(root, n) for n in dirs + files)


def parse(lines, cwd: str) -> dict:
  tap = Tap(cwd)
  error = None
  events = []
  for n, line in enumerate(lines, 1):
    m = LINE_RE.match(line)
    if m:
      events.append(
        (n, int(m.group(1)), m.group(2), m.group(3), int(m.group(4)))
      )
    elif SYSCALL_START_RE.match(line) or "resumed>" in line:
      error = error or f"line {n}: unexpected {line.strip()}"
  if not events:
    error = error or "no syscalls traced"
  for _, pid, name, args, ret in events:
    if name in CLONES and ret > 0:
      tap.parents[ret] = (pid, "CLONE_FS" in args)
  for n, pid, name, args, ret in events:
    try:
      tap.fs(pid)
      if name in CLONES:
        if "CLONE_UNTRACED" in args:
          raise TraceError("untraced child process")
        if ret > 0:
          tap.fs(ret)
        continue
      tap.syscall(pid, name, split_args(args), ret)
    except Exception as e:
      error = error or f"line {n}: {name}: {e!r}"
  tap.add_renamed_dir_contents()
  result = {
    "reads": sorted(tap.reads - tap.writes),
    "writes": sorted(tap.writes),
    "deletes": sorted(tap.deletes),
  }
  if readdirs := tap.readdirs():
    result["readdirs"] = readdirs
  if error:
    result["error"] = error
  return result


def forward_signals(strace: subprocess.Popen):
  """Forwards signals to the command, since strace blocks them."""

  def forward(signum, _):
    try:
      with open(f"/proc/{strace.pid}/task/{strace.pid}/children") as f:
        children = [int(pid) for pid in f.read().split()]
    except OSError:
      children = []
    if not children:
      strace.kill()
    for pid in children:
      try:
        os.kill(pid, signum)
      except ProcessLookupError:
        pass

  for signum in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
    signal.signal(signum, forward)


def main(argv: list[str]) -> int:
  if len(argv) < 4 or argv[0] != "--tap_output" or argv[2] != "--":
    sys.exit("usage: tapcmd.py --tap_output <file> -- <cmd> <args...>")
  tap_output, cmd = argv[1], argv[3:]
  cwd = os.getcwd()
  with tempfile.NamedTemporaryFile(prefix="tapcmd-", suffix=".strace") as trace:
    strace = subprocess.Popen(STRACE + ["-o", trace.name, "--"] + cmd)
    forward_signals(strace)
    returncode = strace.wait()
    with open(trace.name, encoding="ascii", errors="replace") as f:
      result = parse(f, cwd)
  with open(tap_output, "w") as f:
    json.dump(result, f)
  if returncode < 0:
    signal.signal(-returncode, signal.SIG_DFL)
    os.kill(os.getpid(), -returncode)
  return returncode if returncode >= 0 else 128 - returncode


if __name__ == "__main__":
  sys.exit(main(sys.argv[1:]))
