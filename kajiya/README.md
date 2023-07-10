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
