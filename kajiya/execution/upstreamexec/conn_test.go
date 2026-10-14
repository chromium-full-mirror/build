// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package upstreamexec

import "testing"

// TestIsGoogleAPIsEndpoint verifies which endpoint forms Dial recognizes as
// served under googleapis.com, and so pings with RBE's keepalive.
func TestIsGoogleAPIsEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{name: "bare_host_port", endpoint: "remotebuildexecution.googleapis.com:443", want: true},
		{name: "bare_host", endpoint: "remotebuildexecution.googleapis.com", want: true},
		{name: "upper_case", endpoint: "RemoteBuildExecution.GoogleAPIs.com:443", want: true},
		{name: "dns_without_resolver", endpoint: "dns:///remotebuildexecution.googleapis.com:443", want: true},
		{name: "dns_with_resolver", endpoint: "dns://8.8.8.8/remotebuildexecution.googleapis.com:443", want: true},
		{name: "resolver_under_googleapis", endpoint: "dns://dns.googleapis.com/localhost:50051", want: false},
		{name: "local_server", endpoint: "localhost:50051", want: false},
		{name: "suffix_without_dot", endpoint: "evilgoogleapis.com:443", want: false},
		{name: "unix_socket", endpoint: "unix:///tmp/remotebuildexecution.googleapis.com", want: false},
		{name: "unix_socket_relative", endpoint: "unix:remotebuildexecution.googleapis.com", want: false},
		{name: "passthrough", endpoint: "passthrough:///remotebuildexecution.googleapis.com:443", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := isGoogleAPIsEndpoint(tc.endpoint), tc.want; got != want {
				t.Errorf("isGoogleAPIsEndpoint(%q) = %t, want %t", tc.endpoint, got, want)
			}
		})
	}
}
