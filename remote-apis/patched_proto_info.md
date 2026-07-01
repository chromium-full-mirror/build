# go.chromium.org/build/remote-apis

This is a vendored fork of upstream [bazelbuild/remote-apis][upstream], restricted
to the packages siso/kajiya consume, with one local proto patch and a Go module
rename. It is **regenerated mechanically** by [`update.sh`](./update.sh) — do not
hand-edit the generated `*.pb.go` files.

## Pinned upstream

- **Repo:** https://github.com/bazelbuild/remote-apis
- **Commit:** `becdd8f9ff811df88a22d3eadd6341753d51d167` (`main`, 2026-06-30)

Update this commit whenever you re-vendor.

## What this fork does to upstream

1. **Subset.** Only the packages our consumers import are vendored:
   - `build/bazel/remote/execution/v2` (siso + kajiya)
   - `build/bazel/semver` (transitive dependency)

   Upstream's `remote/asset`, `remote/logstream`, BUILD files, test vectors, and
   the cc/go/java helper dirs are intentionally omitted. Add a package to the
   `PKGS` list in `update.sh` if a consumer starts needing it.

2. **One proto patch:** [`patches/remote_apis/remote_execution.proto.patch`](./patches/remote_apis/remote_execution.proto.patch)
   adds an `ActionCache` extension for indexing actions by a custom key:
   - RPCs `AddActionLookup` and `ListActions`.
   - Messages `AddActionLookupRequest`, `AddActionLookupResponse`,
     `ListActionsRequest`, `ListActionsResponse`.

   Consumed by `siso/reapi/action_cache_map.go`. Not upstream.

3. **Module rename.** The generated Go has its module path rewritten
   `github.com/bazelbuild/remote-apis` → `go.chromium.org/build/remote-apis`
   (so it doesn't collide with the canonical module in the proto registry). The
   two paths are deliberately the **same length (33 bytes)**, so the rename is a
   byte-safe `sed` even inside the protobuf `rawDesc` length-prefixed strings.
   The `.proto` files keep upstream's `go_package`; the rename is applied only to
   the generated `.go`.

The `.pb.go` files come from **upstream's own bazel codegen** (the same
`go_proto_library` + `go_grpc_v2` rules as upstream's `./hooks/pre-commit`), so
apart from the renamed `semver` import they match what upstream commits.

## Re-vendoring

```sh
# requires bazelisk and go on PATH (and network access); no checkout needed
./update.sh             # tracks upstream main
./update.sh v2.x.y      # or a specific branch/tag
```

The script shallow-clones upstream into a temp dir under `~/.cache` (not `/tmp`:
Bazel's hermetic sandbox hides `/tmp` workspaces from sandboxed protoc), pins its
HEAD, applies the patch, runs the bazel codegen, copies the proto + generated Go
into this tree with the module rename, and does a sanity `go build`. Afterwards:
bump the pinned commit above, and run `go build ./...` / `go test ./...` in
`../siso` and `../kajiya`.

[upstream]: https://github.com/bazelbuild/remote-apis
