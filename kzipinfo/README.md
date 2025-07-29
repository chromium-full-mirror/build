# kzipinfo

kzipinfo is a tool to help explore kzip files in a human-readable format.

## Alternatives

Use the official `kzip` binary instead for JSON format:

```
$ go run kythe.io/kythe/go/platform/tools/kzip@latest info --input /tmp/input.kzip
{"corpora":{"foo":{"language_required_inputs":{"rust":{"count":2}}, "language_sources":{"rust":{"count":1}}, "language_cu_info":{"rust":{"count":1}}}}, "size":"17056"}
```

```
$ go run kythe.io/kythe/go/platform/tools/kzip@latest view /tmp/input.kzip
{"v_name":{"corpus":"foo", "language":"rust"}, "required_input":[{"v_name":{"corpus":"foo", "path":"foo/bar/baz.rs"}, "info":{"path":"../../foo/bar/baz.rs", "digest":"155112a5bc22f4640911dff4a167721ade0b739eb1eda4d108727044dcfd9918"}}, {"v_name":{"corpus":"foo", "path":"rust-project.json"}, "info":{"path":"rust-project.json", "digest":"f805b985dc4417d44db05516fc0b61c48680b040d551b46c1324718e4fbe0385"}}], "source_file":["../../foo/bar/baz.rs"]}
```

## Examples

```
$ go run . info /tmp/input.kzip
Kzip Info for: /tmp/input.kzip
Size: 17056 bytes
Corpora Breakdown:
Corpus   Language  CU Count  Source Files  Required Inputs
foo      rust      1         1             2
----     ----      ----      ----          ----
Total              1         1             2
```

## Not yet implemented

```
$ go run . ls input.kzip
Compilation Units in /tmp/input.kzip:
Digest                                                            Language  Primary Source        Output Key
f805b985dc4417d44db05516fc0b61c48680b040d551b46c1324718e4fbe0385  rust      ../../foo/bar/baz.rs
```
