# Patched remote_execution.proto in bazel_remote_apis

We have extracted and verified the patch applied to `remote_execution.proto` in the `bazel_remote_apis` module.

## Patch Information

- **Prompt:** apply patches/remote-apis to bazel_remote_apis module and extract protoc compiled *.pb.go
- **Repo:** sso://tbi-internal/artfs
- **Commit:** 2eaa5edbef6dd8e0c4b6f495e5d4e9ee5e28134c
- **Target File in Module:** `build/bazel/remote/execution/v2/remote_execution.proto`
- **Applied in MODULE.bazel via:**
  ```bazel
  archive_override(
      module_name = "bazel_remote_apis",
      patches = [
          "//patches/remote_apis:remote_execution.proto.patch",
      ],
      ...
  )
  ```

## Summary of Changes

The patch adds custom action lookup capabilities to the `ActionCache` service, allowing actions to be indexed and discovered using custom keys instead of just their content hashes.

### Added RPCs to `ActionCache`

1.  **`AddActionLookup`**: Associates a custom lookup key with an action digest.
    ```proto
    rpc AddActionLookup(AddActionLookupRequest) returns (AddActionLookupResponse) {
      option (google.api.http) = {
        post: "/v2/{instance_name=**}/lookups/{lookup_key}:addActionLookup"
        body: "*"
      };
    }
    ```
2.  **`ListActions`**: Retrieves actions associated with a custom lookup key.
    ```proto
    rpc ListActions(ListActionsRequest) returns (ListActionsResponse) {
      option (google.api.http) = {
        get: "/v2/{instance_name=**}/lookups/{lookup_key}:listActions"
      };
    }
    ```

### Added Messages

-   `AddActionLookupRequest`: Contains `instance_name`, `lookup_key`, `action_digest`, and `digest_function`.
-   `AddActionLookupResponse`: Empty response.
-   `ListActionsRequest`: Contains `instance_name`, `lookup_key`, `page_size`, `page_token`, and `digest_function`.
-   `ListActionsResponse`: Contains list of `Action` messages and `next_page_token`.
