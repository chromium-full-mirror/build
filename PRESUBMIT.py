# Copyright 2025 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.
"""Top-level presubmit script for build.

See http://dev.chromium.org/developers/how-tos/depottools/presubmit-scripts for
details on the presubmit API built into gcl.
"""

PRESUBMIT_VERSION = "2.0.0"
USE_PYTHON3 = True

THIRD_PARTY_DIRS = [
  "siso/third_party",
]

# Go version used on presubmit bots, which every go.mod must also require.
GO_VERSION = "1.27.1"

# Repository-relative go.mod path -> Go version, for modules that must differ
# from GO_VERSION. Each entry should have a comment explaining why.
GO_VERSION_EXCEPTIONS = {
  # "example/go.mod": "1.26.0",  # b/XXXXXXXXX: reason
}


def CheckChange(input_api, output_api):
  # Default source file filter doesn't include Go.
  # e.g. CheckChangeHasNoTabs would conflict since gofmt enforces tabs.
  def source_file_filter_incl_go(path):
    return input_api.FilterSourceFile(
      path,
      files_to_check=list(input_api.DEFAULT_FILES_TO_CHECK) + [r".+\.go$"],
      files_to_skip=[r".*pb[^/]*\.go$"]
      + [rf"{d}/.*" for d in THIRD_PARTY_DIRS],
    )

  results = []
  results += input_api.canned_checks.CheckDoNotSubmit(input_api, output_api)
  results += input_api.canned_checks.CheckChangeHasNoTabs(input_api, output_api)
  results += input_api.canned_checks.CheckPatchFormatted(
    input_api, output_api, check_clang_format=False
  )
  results += input_api.canned_checks.CheckChangeHasNoStrayWhitespace(
    input_api, output_api, source_file_filter=source_file_filter_incl_go
  )
  results += input_api.canned_checks.CheckInclusiveLanguage(
    input_api, output_api
  )
  results += input_api.canned_checks.CheckLicense(
    input_api, output_api, source_file_filter=source_file_filter_incl_go
  )

  return results


def _GoModFiles(input_api):
  """Returns sorted repository-relative paths of first-party go.mod files."""
  paths = input_api.subprocess.check_output(
    ["git", "ls-files", "--", ":(glob)**/go.mod"],
    cwd=input_api.change.RepositoryRoot(),
    text=True,
  ).splitlines()
  return sorted(
    path
    for path in paths
    if not any(path.startswith(d + "/") for d in THIRD_PARTY_DIRS)
  )


def CheckGoVersionsConsistent(input_api, output_api):
  # Check every go.mod rather than only affected ones, since the most likely
  # mistake is forgetting to update a module that the change doesn't touch.
  root = input_api.change.RepositoryRoot()
  directive_re = input_api.re.compile(
    r"^\s*(go|toolchain)\s+(\S+)", input_api.re.MULTILINE
  )
  errors = []
  for path in _GoModFiles(input_api):
    want = GO_VERSION_EXCEPTIONS.get(path, GO_VERSION)
    with open(input_api.os_path.join(root, path), encoding="utf-8") as f:
      content = f.read()
    for directive, got in directive_re.findall(content):
      if directive == "toolchain":
        got = got.removeprefix("go")
      if got != want:
        errors.append(f"{path}: {directive} {got} (want {want})")
  if not errors:
    return []
  return [
    output_api.PresubmitError(
      "go.mod Go versions must match GO_VERSION in PRESUBMIT.py, or be "
      "listed in GO_VERSION_EXCEPTIONS.",
      items=errors,
    )
  ]


SUBTEST_CHECK_DIRS = [
  r"^gong/gn/build/ninjawriter$",
  r"^siso(/|$)",
]


def _IsSubtestCheckEnabledForDir(input_api, dirpath):
  return any(
    input_api.re.search(pattern, dirpath) for pattern in SUBTEST_CHECK_DIRS
  )


# Changes to these files can change golangci-lint results for code they don't
# touch (e.g. a Go version bump enables new modernize analyzers), so they make
# golangci-lint run on every Go module instead of only on affected dirs.
FULL_GO_LINT_TRIGGERS = [
  r"PRESUBMIT\.py$",  # GO_VERSION and golangci-lint CIPD versions are pinned here.
  r"\.golangci\.yml$",
  r"(.+/)?go\.mod$",
]


def CheckGoChanges(input_api, output_api):
  def file_filter(path):
    return input_api.FilterSourceFile(
      path,
      files_to_check=[r".*\.go$"],
      files_to_skip=THIRD_PARTY_DIRS + [r".*\.pb\.go$", r".*\.gen\.go$"],
    )

  def full_lint_trigger_filter(path):
    return input_api.FilterSourceFile(
      path,
      files_to_check=FULL_GO_LINT_TRIGGERS,
      files_to_skip=THIRD_PARTY_DIRS,
    )

  affected_files = sorted(
    [
      # TODO(b/430465030): Fix this.
      # pylint: disable=unnecessary-comprehension
      f
      for f in input_api.AffectedFiles(
        include_deletes=False, file_filter=file_filter
      )
    ],
    key=lambda source: source.AbsoluteLocalPath(),
  )
  full_lint_triggers = sorted(
    f.LocalPath()
    for f in input_api.AffectedFiles(file_filter=full_lint_trigger_filter)
  )
  if not affected_files and not full_lint_triggers:
    return []

  results = []

  # Fetch dependencies from CIPD.
  # This is done in this script because we don't use gclient to manage Go
  # dependencies.
  # golangci-lint should always be fetched to ensure errors are consistent
  # between local developer machines and presubmit bots.
  cipd_root = input_api.os_path.join(
    input_api.change.RepositoryRoot(), ".cipd_bin"
  )
  ensure_file_content = (
    "infra/3pp/tools/golangci-lint/${platform} version:3@2.13.1.chromium.1\n"
  )
  go = "go"
  golangci_lint = input_api.os_path.join(cipd_root, "golangci-lint")
  env = input_api.environ.copy()
  if input_api.is_committing and input_api.gerrit:
    # Go is only needed on presubmit bots.
    # This is because we use go.mod to manage the expected Go version on local
    # developer machines, and expect Go to be available on $PATH.
    ensure_file_content += (
      f"infra/3pp/tools/go/${{platform}} version:3@{GO_VERSION}\n"
    )
    go = input_api.os_path.join(cipd_root, "bin", "go")
    env["PATH"] = input_api.os_path.join(cipd_root, "bin") + ":" + env["PATH"]
  if input_api.platform.startswith("linux"):
    ensure_file_content += (
      "infra/3pp/static_libs/libseccomp/${platform} latest\n"
    )
    seccomp_include = input_api.os_path.join(cipd_root, "include")
    seccomp_lib = input_api.os_path.join(cipd_root, "lib")
    seccomp_pkgconfig = input_api.os_path.join(seccomp_lib, "pkgconfig")
    env["CGO_ENABLED"] = "1"
    env["CGO_CFLAGS"] = f"-I{seccomp_include}"
    env["CGO_LDFLAGS"] = f"-L{seccomp_lib} -lseccomp"
    env["PKG_CONFIG_PATH"] = seccomp_pkgconfig + (
      ":" + env["PKG_CONFIG_PATH"] if "PKG_CONFIG_PATH" in env else ""
    )
  input_api.subprocess.check_call(
    [
      "cipd",
      "ensure",
      "-log-level",
      "warning",
      "-root",
      str(cipd_root),
      "-ensure-file",
      "-",
    ],
    stdin=ensure_file_content.encode("utf-8"),
    cwd=input_api.change.RepositoryRoot(),
  )

  # Make sure Go is available on $PATH.
  try:
    input_api.subprocess.check_call(
      [go, "version"],
      stdout=input_api.subprocess.PIPE,
      stderr=input_api.subprocess.PIPE,
    )
  except input_api.subprocess.CalledProcessError as e:
    return [
      output_api.PresubmitPromptOrNotify(
        f"go isn't available on your $PATH: {e}"
      )
    ]

  if input_api.is_committing:
    error_type = output_api.PresubmitError
  else:
    error_type = output_api.PresubmitPromptWarning

  tests = []

  # Build custom AST vettool and run on enabled directories.
  subtestanalyzer_dir = input_api.os_path.join(
    input_api.change.RepositoryRoot(), "infra", "subtestanalyzer"
  )
  subtest_affected_dirs = {
    input_api.os_path.dirname(f.AbsoluteLocalPath()): input_api.os_path.dirname(
      f.LocalPath()
    )
    for f in affected_files
    if _IsSubtestCheckEnabledForDir(
      input_api, input_api.os_path.dirname(f.LocalPath())
    )
  }
  if input_api.os_path.exists(subtestanalyzer_dir) and subtest_affected_dirs:
    vettool_bin = input_api.os_path.join(subtestanalyzer_dir, "subtestanalyzer")
    try:
      input_api.subprocess.check_call(
        [go, "build", "-o", vettool_bin, "."],
        cwd=subtestanalyzer_dir,
        stdout=input_api.subprocess.PIPE,
        stderr=input_api.subprocess.PIPE,
      )
      for absolute, pretty in sorted(subtest_affected_dirs.items()):
        kwargs = {"cwd": absolute}
        if env:
          kwargs["env"] = env
        tests.append(
          input_api.Command(
            name=f"Check subtest names via go vet on {pretty}",
            cmd=[go, "vet", f"-vettool={vettool_bin}", "./..."],
            kwargs=kwargs,
            message=error_type,
          )
        )
    except input_api.subprocess.CalledProcessError as e:
      results.append(
        output_api.PresubmitPromptOrNotify(
          f"Failed to build subtestanalyzer vettool: {e}"
        )
      )

  # Run `golangci-lint` on only changed folders by default, otherwise all modules.
  if not full_lint_triggers:
    lint_dirs = sorted(
      {input_api.os_path.dirname(f.LocalPath()) for f in affected_files}
    )
    lint_pattern = "."
  else:
    lint_dirs = [
      input_api.os_path.dirname(path) for path in _GoModFiles(input_api)
    ]
    lint_pattern = "./..."
    results.append(
      output_api.PresubmitNotifyResult(
        "Running golangci-lint on all Go modules because lint-affecting "
        "files changed.",
        items=full_lint_triggers,
      )
    )
  for lint_dir in lint_dirs:
    kwargs = {
      "cwd": input_api.os_path.join(input_api.change.RepositoryRoot(), lint_dir)
    }
    if env:
      kwargs["env"] = env
    # e.g. "siso/subcmd/ninja" or "siso/...".
    pretty = input_api.os_path.normpath(
      input_api.os_path.join(lint_dir, lint_pattern)
    )
    tests.append(
      input_api.Command(
        name=f"Check golangci-lint on {pretty}",
        cmd=[
          golangci_lint,
          "run",
          "--timeout=15m",
          "--allow-parallel-runners",
          lint_pattern,
        ],
        kwargs=kwargs,
        message=error_type,
      )
    )
  return results + input_api.RunTests(tests)


def CheckPythonChanges(input_api, output_api):
  files_to_skip = list(input_api.DEFAULT_FILES_TO_SKIP)
  files_to_skip += [rf"{d}/.*" for d in THIRD_PARTY_DIRS]
  return input_api.RunTests(
    input_api.canned_checks.GetRuff(
      input_api,
      output_api,
      files_to_skip=files_to_skip,
    )
  )
