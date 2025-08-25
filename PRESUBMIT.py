# Copyright 2025 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.
"""Top-level presubmit script for build.

See http://dev.chromium.org/developers/how-tos/depottools/presubmit-scripts for
details on the presubmit API built into gcl.
"""

PRESUBMIT_VERSION = '2.0.0'
USE_PYTHON3 = True


def CheckChange(input_api, output_api):
  third_party_dirs = [
      'siso/third_party',
  ]
  files_to_skip = lambda path: input_api.FilterSourceFile(
      path,
      files_to_skip=[
          r'.*pb[^/]*\.go$',
      ] + [rf'{d}/.*' for d in third_party_dirs])

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


def CheckGoFormat(input_api, output_api):
  should_check = lambda f: f.LocalPath().endswith(('.go'))
  files = input_api.AffectedFiles(
      file_filter=should_check, include_deletes=False)
  if not files:
    return []

  # If we're running via the presubmit bot, we need to fetch Go from CIPD.
  # (We don't install Go via DEPS.)
  go = 'go'
  gofmt = 'gofmt'
  if input_api.is_committing and input_api.gerrit:
    cipd_root = input_api.os_path.join(input_api.change.RepositoryRoot(),
                                       '.cipd_bin')
    ensure_file_content = 'infra/3pp/tools/go/${platform} version:3@1.25.0\n'
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
    go = input_api.os_path.join(cipd_root, 'bin', 'go')
    gofmt = input_api.os_path.join(cipd_root, 'bin', 'gofmt')

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

  bad = []
  for f in files:
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
  return []
