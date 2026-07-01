#!/usr/bin/env bash
#
# Copyright 2026 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.
#

# Re-vendor go.chromium.org/build/remote-apis from upstream bazelbuild/remote-apis.
#
# This module is upstream remote-apis, restricted to the packages siso/kajiya
# consume (build/bazel/remote/execution/v2 and build/bazel/semver), with:
#
#   1. one local proto patch (patches/remote_apis/remote_execution.proto.patch,
#      the ActionCache AddActionLookup/ListActions extension), and
#   2. the Go module path rewritten github.com/bazelbuild/remote-apis ->
#      go.chromium.org/build/remote-apis.
#
# The two module paths are intentionally the same length (33 bytes), so the
# rename is a byte-safe sed even inside the generated protobuf rawDesc.
#
# The .pb.go files are produced by upstream's own bazel codegen (the same
# go_proto_library + go_grpc_v2 rules as ./hooks/pre-commit), so they match
# upstream byte-for-byte except for the renamed semver import. Do NOT hand-edit
# them; re-run this script instead.
#
# Clones upstream fresh into a temp dir, regenerates, and refreshes the vendored
# files. Requires bazelisk and go on PATH (and network access).
#
# Usage:
#   ./update.sh [UPSTREAM_REF] [UPSTREAM_REPO]
#     UPSTREAM_REF   branch/tag to track (default: main)
#     UPSTREAM_REPO  git URL (default: https://github.com/bazelbuild/remote-apis)
#
# See patched_proto_info.md for the currently-pinned upstream commit and rationale.

set -euo pipefail

VENDOR_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UPSTREAM_REF="${1:-main}"
UPSTREAM_REPO="${2:-https://github.com/bazelbuild/remote-apis}"

OLD_PATH="github.com/bazelbuild/remote-apis"
NEW_PATH="go.chromium.org/build/remote-apis"

# Packages we vendor (proto + generated Go). Add to this list if a consumer
# starts importing another upstream package.
PKGS=(
  "build/bazel/remote/execution/v2"
  "build/bazel/semver"
)

# Clone onto the home filesystem, NOT /tmp: Bazel's hermetic sandbox replaces
# /tmp with an empty tmpfs inside each action, so a workspace under /tmp is
# invisible to sandboxed protoc ("Could not make proto path relative ...").
WORKDIR_BASE="${XDG_CACHE_HOME:-$HOME/.cache}"
mkdir -p "$WORKDIR_BASE"
UPSTREAM="$(mktemp -d "$WORKDIR_BASE/remote-apis-update.XXXXXX")"
cleanup() { rm -rf "$UPSTREAM"; }
trap cleanup EXIT

echo ">> cloning $UPSTREAM_REPO ($UPSTREAM_REF) into $UPSTREAM"
git clone --quiet --single-branch --branch "$UPSTREAM_REF" --depth 1 \
  "$UPSTREAM_REPO" "$UPSTREAM"
UPSTREAM_SHA="$(git -C "$UPSTREAM" rev-parse HEAD)"
echo ">> upstream HEAD: $UPSTREAM_SHA"

echo ">> applying local proto patch"
git -C "$UPSTREAM" apply "$VENDOR_DIR/patches/remote_apis/remote_execution.proto.patch"

echo ">> generating Go via upstream bazel codegen"
( cd "$UPSTREAM"
  USE_BAZEL_VERSION="$(cat .bazelversion)" bazelisk build --output_groups=go_generated_srcs \
    //build/bazel/remote/execution/v2:remote_execution_go_proto \
    //build/bazel/semver:semver_go_proto )
BAZEL_BIN="$(cd "$UPSTREAM" && USE_BAZEL_VERSION="$(cat .bazelversion)" bazelisk info bazel-bin)"

echo ">> copying proto + generated Go into the vendor tree (with module rename)"
for pkg in "${PKGS[@]}"; do
  mkdir -p "$VENDOR_DIR/$pkg"
  # Source .proto: copied verbatim (keeps upstream go_package; codegen remaps it).
  for proto in "$UPSTREAM/$pkg"/*.proto; do
    cp "$proto" "$VENDOR_DIR/$pkg/"
  done
  # Generated .go: pulled from bazel-bin, module path renamed.
  while IFS= read -r gofile; do
    dst="$VENDOR_DIR/$pkg/$(basename "$gofile")"
    sed "s#$OLD_PATH#$NEW_PATH#g" "$gofile" > "$dst"
    chmod u+w "$dst"
  done < <(find "$BAZEL_BIN" -path "*/$pkg/*.pb.go")
done

echo ">> sanity build"
( cd "$VENDOR_DIR" && go build ./... )

echo ">> done. Update the pinned sha in patched_proto_info.md to: $UPSTREAM_SHA"
