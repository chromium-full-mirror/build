#!/bin/bash
#
# Copyright 2025 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.

# Repo root
cd "$(dirname ${BASH_SOURCE[0]})/../.."

if [[ ! -d .jj ]]; then
  jj git init --colocate .
  ln -sf "$(realpath infra/jj/config.toml)" .jj/repo/config.toml
fi

echo "Reminder: If you haven't already, we recommend joining https://groups.google.com/g/chromium-jj-users"
