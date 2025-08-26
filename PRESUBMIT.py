# Copyright 2025 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.
"""Top-level presubmit script for build.

See http://dev.chromium.org/developers/how-tos/depottools/presubmit-scripts for
details on the presubmit API built into gcl.
"""

PRESUBMIT_VERSION = '2.0.0'
USE_PYTHON3 = True

THIRD_PARTY_DIRS = [
    'siso/third_party',
]


def CheckChange(input_api, output_api):
  files_to_skip = lambda path: input_api.FilterSourceFile(
      path,
      files_to_skip=[
          r'.*pb[^/]*\.go$',
      ] + [rf'{d}/.*' for d in THIRD_PARTY_DIRS])

  results = []
  results += input_api.canned_checks.CheckDoNotSubmit(input_api, output_api)
  results += input_api.canned_checks.CheckChangeHasNoTabs(
      input_api, output_api, source_file_filter=files_to_skip)
  results += input_api.canned_checks.CheckChangeHasNoStrayWhitespace(
      input_api, output_api, source_file_filter=files_to_skip)
  results += input_api.canned_checks.CheckInclusiveLanguage(
      input_api, output_api)
  results += input_api.canned_checks.CheckLicense(
      input_api, output_api, source_file_filter=files_to_skip)

  return results


def CheckGoChanges(input_api, output_api):
  file_filter = lambda path: input_api.FilterSourceFile(
      path,
      files_to_check=[r'.*\.go$'],
      files_to_skip=THIRD_PARTY_DIRS + [r'.*\.pb\.go$', r'.*\.gen\.go$'])
  affected_files = sorted([
      f for f in input_api.AffectedFiles(
          include_deletes=False, file_filter=file_filter)
  ],
                          key=lambda source: source.AbsoluteLocalPath())
  if not affected_files:
    return []

  # Fetch dependencies from CIPD.
  # This is done in this script because we don't use gclient to manage Go
  # dependencies.
  # golangci-lint should always be fetched to ensure errors are consistent
  # between local developer machines and presubmit bots.
  cipd_root = input_api.os_path.join(input_api.change.RepositoryRoot(),
                                      '.cipd_bin')
  ensure_file_content = ('infra/3pp/tools/golangci-lint/${platform} '
                         'version:3@2.4.0\n')
  go = 'go'
  gofmt = 'gofmt'
  golangci_lint = input_api.os_path.join(cipd_root, 'golangci-lint')
  env = input_api.environ.copy()
  if input_api.is_committing and input_api.gerrit:
    # Go is only needed on presubmit bots.
    # This is because we use go.mod to manage the expected Go version on local
    # developer machines, and expect Go to be available on $PATH.
    ensure_file_content += ('infra/3pp/tools/go/${platform} '
                            'version:3@1.25.0\n')
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
        output_api.PresubmitError(
            ('Found badly formatted Go file(s). '
             'Run `gofmt -s -w .` to fix them.'), bad)
    ]

  # Run `golangci-lint` on folders.
  dirs = {
      input_api.os_path.dirname(f.AbsoluteLocalPath()): input_api.os_path.dirname(f.LocalPath())
      for f in affected_files
  }
  if input_api.is_committing:
    error_type = output_api.PresubmitError
  else:
    error_type = output_api.PresubmitPromptWarning

  tests = []
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
  return input_api.RunTests(tests)
