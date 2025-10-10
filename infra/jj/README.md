# jj for build.git

jj is a Git-compatible version control system that is not officially supported
by ChOps at time of writing.

As of jj v0.34.0, `jj gerrit upload` is available, making a vanilla jj workflow
somewhat usable. This folder contains configuration and scripts to further
improve the default experience.

## Switching from git

Supported:

- `jj fix` as a replacement for `git cl format`
  - Go, Python only

Not yet supported:

- `jj [cr-]sync`
  - For now, `git fetch origin main && jj rebase --skip-emptied -b "mutable()" -d "trunk()"`
- `jj [cr-]upload`
  - For now, use `jj gerrit upload`. You can try running `git cl presubmit` manually beforehand.
- `jj [cr-]bug`
  - For now, manually add `Bug: ...` to your commit descriptions.
