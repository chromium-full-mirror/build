// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package upstreamexec

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/credentials/oauth"
	"google.golang.org/grpc/keepalive"
)

// maxCallMsgSize is the gRPC message size limit for both directions of every
// call on the upstream connection. It exists for responses, where gRPC's
// default 4 MiB receive limit would fail a large but valid message, such as
// an ExecuteResponse whose ActionResult lists tens of thousands of outputs.
// 16 MiB leaves room for those while still bounding the memory a single
// malformed response can take. Requests stay far below it, because batches
// are capped at defaultMaxBatchSize and maxBatchBlobs, and ByteStream chunks
// at byteStreamChunkSize.
const maxCallMsgSize = 16 * 1024 * 1024

// serviceConfig enables round_robin load balancing across all addresses the
// DNS resolver returns for the upstream endpoint. RBE's frontend resolves to
// several GFE addresses, and gRPC's default pick_first pins every RPC to one
// of them. With a single address, round_robin behaves like pick_first.
const serviceConfig = `{"loadBalancingConfig":[{"round_robin":{}}]}`

// rbeKeepalive pings an idle connection to RBE's frontend, so that a dead
// transport fails its calls instead of stalling them. These are Siso's
// values, which RBE's frontend accepts.
var rbeKeepalive = keepalive.ClientParameters{
	Time:    30 * time.Second,
	Timeout: 20 * time.Second,
}

// Dial constructs a gRPC connection to an upstream REAPI service. An endpoint
// without a URI scheme is resolved through DNS.
//
// If insecureMode is true, the connection uses neither transport security nor
// per-RPC credentials, which suits an upstream on a trusted network such as a
// local test server. Otherwise it uses TLS with the system root CAs and
// attaches an OAuth2 access token from `luci-auth` to every call; Dial fails
// if luci-auth is not on PATH.
func Dial(endpoint string, insecureMode bool) (*grpc.ClientConn, error) {
	// Configure message limits and load balancing.
	opts := []grpc.DialOption{
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxCallMsgSize),
			grpc.MaxCallSendMsgSize(maxCallMsgSize),
		),
		grpc.WithDefaultServiceConfig(serviceConfig),
	}

	// Ping only an endpoint under googleapis.com, where RBE's frontend
	// accepts these pings. Other servers enforce their own ping policy, and
	// one that allows fewer pings closes the connection with GOAWAY
	// too_many_pings.
	if isGoogleAPIsEndpoint(endpoint) {
		opts = append(opts, grpc.WithKeepaliveParams(rbeKeepalive))
	}

	// Configure transport security and credentials.
	if insecureMode {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		// An empty tls.Config verifies the server against the system root CAs.
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))

		ts, err := newLuciAuthTokenSource()
		if err != nil {
			return nil, fmt.Errorf("upstreamexec.Dial: %w", err)
		}
		opts = append(opts, grpc.WithPerRPCCredentials(oauth.TokenSource{TokenSource: ts}))
	}

	// Create the client. grpc.NewClient defaults to the dns resolver; the
	// explicit scheme keeps round_robin's one subchannel per resolved address
	// independent of that default.
	target := endpoint
	if !strings.Contains(target, "://") {
		target = "dns:///" + target
	}
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("upstreamexec.Dial: failed to dial %q: %w", endpoint, err)
	}
	return conn, nil
}

// isGoogleAPIsEndpoint reports whether the service host of endpoint, as Dial
// takes it, ends in ".googleapis.com". The host comes from a bare host:port,
// or from the path of a dns URI such as "dns:///host:port" or
// "dns://resolver/host:port". The authority of a dns URI names the DNS
// server, not the service, so it never counts.
func isGoogleAPIsEndpoint(endpoint string) bool {
	// Find the service's host:port.
	hostport := endpoint
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "dns" {
			return false
		}
		hostport = strings.TrimPrefix(u.Path, "/")
	}

	// Strip the port, which is optional.
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	return strings.HasSuffix(strings.ToLower(host), ".googleapis.com")
}
