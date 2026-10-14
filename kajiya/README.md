# 🔥 鍛冶屋 (Kajiya)

Kajiya is an RBE-compatible REAPI backend implementation used as a testing
server during development of Chromium's new build tooling. It is not meant
for production use, but can be very useful for local testing of any remote
execution related code.

## How to use

```shell
$ go build && ./kajiya

# Build Bazel using kajiya as the backend.
$ bazel build --remote_executor=grpc://localhost:50051 //src:bazel

# Build Chromium with autoninja + reclient using kajiya as the backend.
$ gn gen out/default --args="use_remoteexec=true"
$ env \
    RBE_automatic_auth=false \
    RBE_service="localhost:50051" \
    RBE_service_no_security=true \
    RBE_service_no_auth=true \
    RBE_compression_threshold=-1 \
    autoninja -C out/default -j $(nproc) chrome
```

## Features

Kajiya can act as an REAPI remote cache and/or remote executor. By default, both
services are provided, but you can also run an executor without a cache, or a
cache without an executor:

```shell
# Remote execution without caching
$ ./kajiya -cache=false

# Remote caching without execution (clients must upload action results)
$ ./kajiya -execution=false
```

## Network emulation

As part of evaluating the performance of remote execution, it's important
to check how the system behaves in different network conditions. For example,
if your remote execution backend were hosted in a US cloud region, then you
could use these parameters to simulate real user conditions:

- User is in same region: ~15ms latency
- User is in Tokyo: ~200ms latency

On Linux, we can use `tc-netem` to emulate such conditions when testing on
`localhost`. Please note that it will affect all traffic on localhost, not
just the remote execution one, so remember to remove it again after your
benchmark.

```shell
# Parameters
bandwidth="1000mbit"
latency="200ms"
jitter="50ms"
correlation="25%"

# Enable network emulation on localhost
sudo tc qdisc add dev lo root netem \
  delay ${latency} ${jitter} ${correlation} \
  rate ${bandwidth}

# Remove network emulation
sudo tc qdisc del dev lo root

# Check current status
tc -s qdisc ls dev lo
```

## RBE proxy mode

With `-upstream_proxy`, Kajiya forwards Execute requests to an upstream REAPI
service, such as Google RBE, instead of running actions locally. The CAS and
the action cache stay local. Kajiya uploads the inputs the upstream is
missing, downloads every output into its local CAS, and serves later requests
for the same action from its local action cache without contacting the
upstream. It remembers which blobs the upstream has, so inputs it has seen
before cost no FindMissingBlobs call, and re-uploads blobs that the upstream
has evicted.

```shell
$ luci-auth login -scopes "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email"
$ ./kajiya -upstream_proxy \
    -upstream_reapi=remotebuildexecution.googleapis.com:443 \
    -upstream_instance=projects/rbe-chromium-untrusted/instances/default_instance
```

- `-upstream_reapi` is the upstream endpoint. It defaults to Google RBE.
- `-upstream_instance` is the upstream instance name. It defaults to
  Chromium's untrusted RBE instance.
- `-upstream_insecure` connects without TLS and credentials, for example to a
  second Kajiya on a trusted network. Otherwise Kajiya authenticates with
  OAuth2 tokens from `luci-auth`, which must be on `PATH` and logged in with
  the scopes above.

At startup, Kajiya fails if it can't reach the upstream or if no digest
function is supported by both sides. `ExecutionCapabilities.digest_functions`
lists only the configured functions that the upstream supports for both its
cache and execution; the cache keeps serving all configured functions. The local
execution flags `-sandbox`, `-allow_host_fs` and `-trace_inputs` are ignored.

### Testing

To run the tests, and optionally the live test against RBE:

```shell
$ go test ./execution/upstreamexec/...

# Run the live RBE test against a real upstream REAPI service. The test is
# skipped unless KAJIYA_TEST_RBE=1 explicitly enables it, and it needs an
# active luci-auth session for credentials.
$ luci-auth login -scopes "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email"
$ env \
    KAJIYA_TEST_RBE=1 \
    KAJIYA_TEST_RBE_ENDPOINT=remotebuildexecution.googleapis.com:443 \
    KAJIYA_TEST_RBE_INSTANCE=projects/rbe-chromium-untrusted/instances/default_instance \
    KAJIYA_TEST_RBE_CONTAINER_IMAGE=docker://gcr.io/chops-public-images-prod/rbe/siso-chromium/linux:latest \
    go test -run TestExecuteOnRBE ./execution/upstreamexec/
```
