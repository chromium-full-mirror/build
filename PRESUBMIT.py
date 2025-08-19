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
      input_api,
      output_api,
      excluded_directories_relative_path=third_party_dirs)
  results += input_api.canned_checks.CheckLicense(
      input_api, output_api, source_file_filter=files_to_skip)

  return results
