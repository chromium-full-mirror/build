# Copyright 2025 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.
"""Top-level presubmit script for build.

See http://dev.chromium.org/developers/how-tos/depottools/presubmit-scripts for
details on the presubmit API built into gcl.
"""

PRESUBMIT_VERSION = '2.0.0'
USE_PYTHON3 = True

DISABLED_PYLINT_WARNINGS = [
    'no-init',  # Class has no __init__ method
    'super-init-not-called',  # __init__ method from base class is not called
    'cyclic-import',
    'unused-argument',
    'import-outside-toplevel',
    'inconsistent-return-statements',
    'no-member',
    'no-value-for-parameter',
    'stop-iteration-return',
    'subprocess-run-check',
    # TODO(crbug.com/441191932): Re-enable these checks.
    # ************* Module action
    # siso/subcmd/ninja/testdata/TestBuild_CopyLocalOut/tools/action.py:21:8: W0612: Unused variable 'i' (unused-variable)
    # ************* Module clang++
    # siso/subcmd/ninja/testdata/TestBuild_Cleandead/tools/clang++.py:24:12: W0622: Redefining built-in 'input' (redefined-builtin)
    # ************* Module link
    # siso/subcmd/ninja/testdata/TestBuild_EdgeRule/tools/link.py:19:6: W0622: Redefining built-in 'input' (redefined-builtin)
    # siso/subcmd/ninja/testdata/TestBuild_EdgeRule_solibs/tools/link.py:19:6: W0622: Redefining built-in 'input' (redefined-builtin)
    # siso/subcmd/ninja/testdata/TestBuild_EdgeRule_solibs_recursive/tools/link.py:19:6: W0622: Redefining built-in 'input' (redefined-builtin)
    # siso/subcmd/ninja/testdata/TestBuild_EdgeRule_stamp_solibs/tools/link.py:20:6: W0622: Redefining built-in 'input' (redefined-builtin)
    # ************* Module ar
    # siso/subcmd/ninja/testdata/TestBuild_EdgeRule/tools/ar.py:20:6: W0622: Redefining built-in 'input' (redefined-builtin)
    # siso/subcmd/ninja/testdata/TestBuild_Local_Inputs/tools/action.py:17:6: W0622: Redefining built-in 'input' (redefined-builtin)
    # ************* Module gen
    # siso/subcmd/ninja/testdata/TestBuild_RemovedArtifact/tools/gen.py:8:0: W0622: Redefining built-in 'input' (redefined-builtin)
    # ************* Module archive
    # siso/subcmd/ninja/testdata/TestBuild_OutputDir/tools/archive.py:19:8: W0622: Redefining built-in 'input' (redefined-builtin)
    # siso/subcmd/ninja/testdata/TestBuild_Restat/tools/action.py:17:6: W0622: Redefining built-in 'input' (redefined-builtin)
    # siso/subcmd/ninja/testdata/TestBuild_RestatMultiout/tools/action.py:18:6: W0622: Redefining built-in 'input' (redefined-builtin)
    # siso/subcmd/ninja/testdata/TestBuild_Restat_RestatContent/tools/action.py:17:6: W0622: Redefining built-in 'input' (redefined-builtin)
    # ************* Module action2
    # siso/subcmd/ninja/testdata/TestBuild_RestatMultiout/tools/action2.py:17:6: W0622: Redefining built-in 'input' (redefined-builtin)
    # siso/subcmd/ninja/testdata/TestBuild_offline/tools/gen.py:9:0: W0622: Redefining built-in 'input' (redefined-builtin)
    'redefined-builtin',
    # ************* Module action
    # siso/subcmd/ninja/testdata/TestBuild_CopyLocalOut/tools/action.py:21:8: W0612: Unused variable 'i' (unused-variable)
    'unused-variable',
]

THIRD_PARTY_DIRS = [
    'siso/third_party',
]


def CheckChange(input_api, output_api):
  # Default source file filter doesn't include Go.
  # e.g. CheckChangeHasNoTabs would conflict since gofmt enforces tabs.
  source_file_filter_incl_go = lambda path: input_api.FilterSourceFile(
      path,
      files_to_check=list(input_api.DEFAULT_FILES_TO_CHECK) + [
          r'.+\.go$',
      ],
      files_to_skip=[
          r'.*pb[^/]*\.go$',
      ] + [rf'{d}/.*' for d in THIRD_PARTY_DIRS])

  results = []
  results += input_api.canned_checks.CheckDoNotSubmit(input_api, output_api)
  results += input_api.canned_checks.CheckChangeHasNoTabs(
      input_api, output_api)
  results += input_api.canned_checks.CheckPatchFormatted(
      input_api, output_api, check_clang_format=False)
  results += input_api.canned_checks.CheckChangeHasNoStrayWhitespace(
      input_api, output_api, source_file_filter=source_file_filter_incl_go)
  results += input_api.canned_checks.CheckInclusiveLanguage(
      input_api, output_api)
  results += input_api.canned_checks.CheckLicense(
      input_api, output_api, source_file_filter=source_file_filter_incl_go)

  return results


SUBTEST_CHECK_DIRS = [
    r'^gong/gn/build/ninjawriter$',
    r'^siso/execute$',
    r'^siso/reapi/merkletree$',
    r'^siso/toolsupport/nsjailutil$',
]


def _IsSubtestCheckEnabledForDir(input_api, dirpath):
  return any(
      input_api.re.search(pattern, dirpath)
      for pattern in SUBTEST_CHECK_DIRS)


def CheckGoChanges(input_api, output_api):
  file_filter = lambda path: input_api.FilterSourceFile(
      path,
      files_to_check=[r'.*\.go$'],
      files_to_skip=THIRD_PARTY_DIRS + [r'.*\.pb\.go$', r'.*\.gen\.go$'])
  affected_files = sorted(
      [
          # TODO(b/430465030): Fix this.
          # pylint: disable=unnecessary-comprehension
          f for f in input_api.AffectedFiles(
              include_deletes=False, file_filter=file_filter)
      ],
      key=lambda source: source.AbsoluteLocalPath())
  if not affected_files:
    return []

  results = []

  # Fetch dependencies from CIPD.
  # This is done in this script because we don't use gclient to manage Go
  # dependencies.
  # golangci-lint should always be fetched to ensure errors are consistent
  # between local developer machines and presubmit bots.
  cipd_root = input_api.os_path.join(input_api.change.RepositoryRoot(),
                                     '.cipd_bin')
  ensure_file_content = ('infra/3pp/tools/golangci-lint/${platform} '
                         'version:3@2.13.1.chromium.1\n')
  go = 'go'
  gofmt = 'gofmt'
  golangci_lint = input_api.os_path.join(cipd_root, 'golangci-lint')
  env = input_api.environ.copy()
  if input_api.is_committing and input_api.gerrit:
    # Go is only needed on presubmit bots.
    # This is because we use go.mod to manage the expected Go version on local
    # developer machines, and expect Go to be available on $PATH.
    ensure_file_content += ('infra/3pp/tools/go/${platform} '
                            'version:3@1.26.6\n')
    go = input_api.os_path.join(cipd_root, 'bin', 'go')
    gofmt = input_api.os_path.join(cipd_root, 'bin', 'gofmt')
    env['PATH'] = input_api.os_path.join(cipd_root, 'bin') + ':' + env['PATH']
  input_api.subprocess.check_call(
      [
          'cipd',
          'ensure',
          '-log-level',
          'warning',
          '-root',
          str(cipd_root),
          '-ensure-file',
          '-',
      ],
      stdin=ensure_file_content.encode('utf-8'),
      cwd=input_api.change.RepositoryRoot(),
  )

  # Make sure Go is available on $PATH.
  try:
    input_api.subprocess.check_call([go, 'version'],
                                    stdout=input_api.subprocess.PIPE,
                                    stderr=input_api.subprocess.PIPE)
  except input_api.subprocess.CalledProcessError as e:
    return [
        output_api.PresubmitPromptOrNotify(
            f"go isn't available on your $PATH: {e}")
    ]

  # Run `go fmt` on files individually.
  bad = []
  for f in affected_files:
    try:
      stdout, _ = input_api.subprocess.check_call_out(
          [gofmt, '-s', '-d', f.LocalPath()],
          stdout=input_api.subprocess.PIPE,
          stderr=input_api.subprocess.PIPE)
      if stdout.strip():
        bad.append(f)
    except input_api.subprocess.CalledProcessError as e:
      return [output_api.PresubmitError(f'gofmt failed to run: {e}')]
  if bad:
    return [
        output_api.PresubmitError(('Found badly formatted Go file(s). '
                                   'Run `gofmt -s -w .` to fix them.'), bad)
    ]

  dirs = {
      input_api.os_path.dirname(f.AbsoluteLocalPath()):
          input_api.os_path.dirname(f.LocalPath()) for f in affected_files
  }
  if input_api.is_committing:
    error_type = output_api.PresubmitError
  else:
    error_type = output_api.PresubmitPromptWarning

  tests = []

  # Build custom AST vettool and run on enabled directories.
  subtestanalyzer_dir = input_api.os_path.join(
      input_api.change.RepositoryRoot(), 'infra', 'subtestanalyzer')
  subtest_affected_dirs = {
      input_api.os_path.dirname(f.AbsoluteLocalPath()):
          input_api.os_path.dirname(f.LocalPath())
      for f in affected_files
      if _IsSubtestCheckEnabledForDir(
          input_api, input_api.os_path.dirname(f.LocalPath()))
  }
  if input_api.os_path.exists(subtestanalyzer_dir) and subtest_affected_dirs:
    vettool_bin = input_api.os_path.join(subtestanalyzer_dir, 'subtestanalyzer')
    try:
      input_api.subprocess.check_call(
          [go, 'build', '-o', vettool_bin, '.'],
          cwd=subtestanalyzer_dir,
          stdout=input_api.subprocess.PIPE,
          stderr=input_api.subprocess.PIPE)
      for absolute, pretty in sorted(subtest_affected_dirs.items()):
        kwargs = {'cwd': absolute}
        if env:
          kwargs['env'] = env
        tests.append(
            input_api.Command(
                name=f'Check subtest names via go vet on {pretty}',
                cmd=[go, 'vet', f'-vettool={vettool_bin}', './...'],
                kwargs=kwargs,
                message=error_type))
    except input_api.subprocess.CalledProcessError as e:
      results.append(output_api.PresubmitPromptOrNotify(
          f'Failed to build subtestanalyzer vettool: {e}'))

  # Run `golangci-lint` on folders.
  for absolute, pretty in sorted(dirs.items()):
    kwargs = {'cwd': absolute}
    if env:
      kwargs['env'] = env
    tests.append(
        input_api.Command(
            name=f'Check golangci-lint on {pretty}',
            cmd=[
                golangci_lint, 'run', '--timeout=15m',
                '--allow-parallel-runners', '.'
            ],
            kwargs=kwargs,
            message=error_type))
  return results + input_api.RunTests(tests)


def CheckPythonChanges(input_api, output_api):
  files_to_skip = list(input_api.DEFAULT_FILES_TO_SKIP)
  files_to_skip += [rf'{d}/.*' for d in THIRD_PARTY_DIRS]
  files_to_skip += _IgnoredPaths(input_api)
  return input_api.RunTests(
      input_api.canned_checks.GetPylint(
          input_api,
          output_api,
          files_to_skip=files_to_skip,
          disabled_warnings=DISABLED_PYLINT_WARNINGS,
          version='2.7',
      ))


def _IgnoredPaths(input_api):
  # This computes the list if repository-root-relative paths which are
  # ignored by .gitignore files. There is probably a faster way to do this.
  status_output = input_api.subprocess.check_output(
      ['git', 'status', '--porcelain', '--ignored'], text=True)
  statuses = [(line[:2], line[3:]) for line in status_output.splitlines()]
  return [
      input_api.re.escape(path)
      for (mode, path) in statuses
      if mode in ('!!', '??') and not path.endswith('.pyc')
  ]
