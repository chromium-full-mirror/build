#!/usr/bin/env python3
# Copyright 2026 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.
"""Tests for tapcmd.py. Linux only, needs strace."""

import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest

import tapcmd

TAPCMD = os.path.join(os.path.dirname(os.path.abspath(__file__)), "tapcmd.py")


def hexstr(s: str) -> str:
  return "".join(f"\\x{b:02x}" for b in s.encode())


class ParseTest(unittest.TestCase):
  def parse(self, *lines: str) -> dict:
    return tapcmd.parse([line + "\n" for line in lines], "/ws")

  def test_relative_to_cwd(self):
    result = self.parse(
      f'100 execve("{hexstr("/bin/tool")}", [], 0x0 /* 0 vars */) = 0',
      f'100 open("{hexstr("in")}", O_RDONLY) = 3',
      f'100 creat("{hexstr("out")}", 0644) = 3',
    )
    self.assertEqual(
      result,
      {
        "reads": ["/bin/tool", "/ws/in"],
        "writes": ["/ws/out"],
        "deletes": [],
      },
    )

  def test_child_before_clone_line(self):
    result = self.parse(
      f'100 chdir("{hexstr("sub")}") = 0',
      f'101 unlink("{hexstr("x")}") = 0',
      "100 clone(child_stack=NULL, flags=SIGCHLD) = 101",
      f'100 chdir("{hexstr("/elsewhere")}") = 0',
    )
    self.assertNotIn("error", result)
    self.assertEqual(result["deletes"], ["/ws/sub/x"])

  def test_errors(self):
    for line in [
      "100 io_uring_setup(8, {flags=0}) = 3",
      f'100 chroot("{hexstr("/jail")}") = 0',
      "100 clone(child_stack=NULL, flags=CLONE_UNTRACED|SIGCHLD) = 101",
      f'100 openat(3, "{hexstr("x")}", O_RDONLY) = 4',
      "100 open(0x7ffd0000, O_RDONLY) = 3",
      '100 open("\\xff", O_RDONLY) = 3',
      "100 <... openat resumed>) = 3",
    ]:
      with self.subTest(line=line):
        self.assertIn("error", self.parse(line))
    result = self.parse(
      f'100 open("{hexstr("a")}", O_RDONLY) = 3',
      f'200 open("{hexstr("b")}", O_RDONLY) = 3',
    )
    self.assertIn("unknown process 200", result["error"])


@unittest.skipUnless(shutil.which("strace"), "needs strace")
class TapCmdTest(unittest.TestCase):
  def setUp(self):
    tmp = tempfile.TemporaryDirectory(prefix="tapcmd_test-")
    self.addCleanup(tmp.cleanup)
    self.ws = os.path.realpath(tmp.name)
    os.mkdir(self.p("sub"))
    for name in ("in", "sub/in2", "gone", "sub/x", "sub/y"):
      with open(self.p(name), "w") as f:
        f.write(name)

  def p(self, *names: str) -> str:
    return os.path.join(self.ws, *names)

  def tap(self, *cmd: str) -> tuple[int, dict]:
    out = self.p("tap.json")
    proc = subprocess.run(
      [sys.executable, TAPCMD, "--tap_output", out, "--", *cmd],
      cwd=self.ws,
    )
    with open(out) as f:
      result = json.load(f)
    os.remove(out)
    return proc.returncode, result

  def python(self, code: str) -> dict:
    returncode, result = self.tap(
      sys.executable, "-c", "import os\n" + textwrap.dedent(code)
    )
    self.assertEqual(returncode, 0)
    self.assertNotIn("error", result)
    return result

  def assertSubset(self, paths, got):
    self.assertLessEqual({self.p(p) for p in paths}, set(got))

  def test_shell(self):
    returncode, result = self.tap("sh", "-c", "cat in > out; rm gone")
    self.assertEqual(returncode, 0)
    self.assertNotIn("error", result)
    self.assertSubset(["in"], result["reads"])
    self.assertSubset(["out"], result["writes"])
    self.assertSubset(["gone"], result["deletes"])
    self.assertIn("/usr/bin/cat", result["reads"])

  def test_exit_code(self):
    self.assertEqual(self.tap("sh", "-c", "exit 3")[0], 3)
    self.assertEqual(self.tap("sh", "-c", "kill -TERM $$")[0], -signal.SIGTERM)

  def test_command_args_pass_through(self):
    returncode, _ = self.tap(
      "sh",
      "-c",
      'test "$*" = "--tap_output x --"',
      "sh",
      "--tap_output",
      "x",
      "--",
    )
    self.assertEqual(returncode, 0)

  def test_relative_paths_after_chdir(self):
    result = self.python("""
        os.chdir("sub")
        os.mkdir("d")
        os.unlink("x")
        os.rename("y", "z")
        """)
    self.assertSubset(["sub/d", "sub/z"], result["writes"])
    self.assertSubset(["sub/x", "sub/y"], result["deletes"])

  def test_children_inherit_cwd(self):
    result = self.python("""
        import subprocess
        subprocess.run(["mkdir", "child"], cwd="sub", check=True)
        subprocess.run(["sh", "-c", "mkdir grandchild"], cwd="sub", check=True)
        """)
    self.assertSubset(["sub/child", "sub/grandchild"], result["writes"])

  def test_threads_share_cwd(self):
    result = self.python("""
        import threading
        t = threading.Thread(target=os.chdir, args=("sub",))
        t.start()
        t.join()
        os.mkdir("d")
        """)
    self.assertSubset(["sub/d"], result["writes"])

  def test_dirfd(self):
    result = self.python("""
        fd = os.open("sub", os.O_RDONLY | os.O_DIRECTORY)
        os.close(os.open("in2", os.O_RDONLY, dir_fd=fd))
        os.mkdir("d", dir_fd=fd)
        """)
    self.assertSubset(["sub/in2"], result["reads"])
    self.assertSubset(["sub/d"], result["writes"])

  def test_executed_file_is_read(self):
    with open(self.p("tool.sh"), "w") as f:
      f.write("#!/bin/sh\n")
    os.chmod(self.p("tool.sh"), 0o755)
    returncode, result = self.tap("./tool.sh")
    self.assertEqual(returncode, 0)
    self.assertSubset(["tool.sh"], result["reads"])

  def test_hardlink_source_is_read(self):
    result = self.python('os.link("in", "out")')
    self.assertSubset(["in"], result["reads"])
    self.assertSubset(["out"], result["writes"])

  def test_written_files_are_not_read(self):
    result = self.python("""
        open("in").read()
        open("in", "w").write("new")
        open("a", "w").write("a")
        open("a").read()
        open("sub/in2", "a").write("more")
        open("gone").read()
        """)
    written = {self.p(p) for p in ("in", "a", "sub/in2")}
    self.assertLessEqual(written, set(result["writes"]))
    self.assertFalse(written & set(result["reads"]))
    self.assertSubset(["gone"], result["reads"])

  def test_renamed_dir_contents(self):
    result = self.python("""
        os.makedirs("tmp/s")
        open("tmp/a", "w").close()
        open("tmp/s/b", "w").close()
        os.symlink("a", "tmp/l")
        os.rename("tmp", "outdir")
        """)
    self.assertSubset(
      ["outdir", "outdir/a", "outdir/s", "outdir/s/b", "outdir/l"],
      result["writes"],
    )
    self.assertSubset(["tmp"], result["deletes"])

  def test_concurrency(self):
    for i in range(400):
      with open(self.p(f"f{i}"), "w") as f:
        f.write(str(i))
    result = self.python("""
        import subprocess, threading
        def read(start):
          for i in range(start, 400, 8):
            open(f"f{i}").read()
        threads = [threading.Thread(target=read, args=(i,)) for i in range(4)]
        procs = [
            subprocess.Popen(["sh", "-c", f"for i in $(seq {i} 8 399); do cat f$i; done"],
                             stdout=subprocess.DEVNULL)
            for i in range(4, 8)
        ]
        for t in threads:
          t.start()
        for t in threads:
          t.join()
        for p in procs:
          p.wait()
        """)
    self.assertSubset([f"f{i}" for i in range(400)], result["reads"])

  def test_stat_is_read(self):
    result = self.python("""
        os.path.exists("in")
        os.path.isdir("sub")
        os.access("gone", os.R_OK)
        """)
    self.assertSubset(["in", "sub", "gone"], result["reads"])

  def test_listings(self):
    for d in ("listed/sub", "mixed", "prune", "real", "existing"):
      os.makedirs(self.p(d))
    for f in (
      "listed/a.txt",
      "mixed/old.txt",
      "mixed/gone.txt",
      "prune/a.txt",
      "real/keep.txt",
      "existing/e.txt",
    ):
      with open(self.p(f), "w") as fh:
        fh.write(f)
    os.symlink("a.txt", self.p("listed/link"))
    os.symlink("real", self.p("link"))
    result = self.python("""
        os.listdir("listed")
        os.mkdir("listed/../created")
        open("created/x", "w").close()
        os.listdir("created")
        open("mixed/new.txt", "w").close()
        os.unlink("mixed/gone.txt")
        os.listdir("mixed")
        os.listdir("prune")
        os.unlink("prune/a.txt")
        open("link/w.txt", "w").close()
        os.listdir("real")
        os.makedirs("existing", exist_ok=True)
        os.listdir("existing")
        """)
    self.assertNotIn(self.p("existing"), result["writes"])
    got = {
      os.path.basename(r["dir"]): r
      for r in result["readdirs"]
      if os.path.dirname(r["dir"]) == self.ws
    }
    want = {
      "existing": {"dir": self.p("existing"), "entries": [{"name": "e.txt"}]},
      "listed": {
        "dir": self.p("listed"),
        "entries": [
          {"name": "a.txt"},
          {"name": "link", "type": "SYMLINK"},
          {"name": "sub", "type": "DIR"},
        ],
      },
      "mixed": {
        "dir": self.p("mixed"),
        "entries": [{"name": "old.txt"}],
        "ignored": ["gone.txt", "new.txt"],
      },
      "prune": {"dir": self.p("prune"), "entries": [{"name": "a.txt"}]},
      "real": {
        "dir": self.p("real"),
        "entries": [{"name": "keep.txt"}],
        "ignored": ["w.txt"],
      },
    }
    self.assertEqual(got, want)

  def test_non_utf8_path_is_an_error(self):
    returncode, result = self.tap(
      sys.executable, "-c", "open(b'\\xff', 'w').close()"
    )
    self.assertEqual(returncode, 0)
    self.assertIn("non-UTF-8", result["error"])

  def test_forwards_signals(self):
    out = self.p("tap.json")
    proc = subprocess.Popen(
      [sys.executable, TAPCMD, "--tap_output", out, "--", "sleep", "30"],
      cwd=self.ws,
    )
    time.sleep(1)
    start = time.monotonic()
    proc.send_signal(signal.SIGINT)
    self.assertEqual(proc.wait(timeout=10), -signal.SIGINT)
    self.assertLess(time.monotonic() - start, 5)


if __name__ == "__main__":
  unittest.main()
