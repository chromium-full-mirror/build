# Copyright 2026 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.

load("@builtin//encoding.star", "json")
load("@builtin//struct.star", "module")

def init(ctx):
    filegroups = {
        "toolchain:headers": {
            "type": "glob",
            "includes": [
                "*.h",
                "bin/clang++",
            ],
        },
    }
    step_config = {
        "platforms": {
            "default": {
                "OSFamily": "Linux",
                "container-image": "docker://gcr.io/test/test",
            },
        },
        "rules": [
            {
                "name": "cxx",
                "action": "cxx",
                "inputs": ["toolchain/bin/clang++"],
                "remote": True,
                "remote_inputs": {
                    "toolchain/bin/clang++": "remote-toolchain/bin/clang++",
                    "toolchain/bin/tool": "remote-toolchain/bin/tool",
                },
            },
        ],
        "input_deps": {},
    }

    return module(
        "config",
        step_config = json.encode(step_config),
        filegroups = filegroups,
        handlers = {},
    )
