// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package collector

import (
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/configoptional"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/otlpreceiver"
)

type otlpFactory struct {
	receiver.Factory
	otelSocket string
}

func (f *otlpFactory) CreateDefaultConfig() component.Config {
	cfg := f.Factory.CreateDefaultConfig().(*otlpreceiver.Config)

	grpcCfg := cfg.GRPC.GetOrInsertDefault()
	if f.otelSocket != "" {
		grpcCfg.NetAddr.Endpoint = f.otelSocket
		grpcCfg.NetAddr.Transport = "unix"
	} else {
		// this is default, but to be explicit.
		grpcCfg.NetAddr.Endpoint = "127.0.0.1:4317"
		grpcCfg.NetAddr.Transport = "tcp"
	}
	cfg.GRPC = configoptional.Default(*grpcCfg)
	// Disable HTTP.
	cfg.HTTP = configoptional.Optional[otlpreceiver.HTTPConfig]{}

	return cfg
}
