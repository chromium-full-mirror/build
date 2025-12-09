// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.
package collector

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/google/subcommands"
	"go.opentelemetry.io/collector/confmap"
	envprovider "go.opentelemetry.io/collector/confmap/provider/envprovider"
	fileprovider "go.opentelemetry.io/collector/confmap/provider/fileprovider"
	httpprovider "go.opentelemetry.io/collector/confmap/provider/httpprovider"
	httpsprovider "go.opentelemetry.io/collector/confmap/provider/httpsprovider"
	yamlprovider "go.opentelemetry.io/collector/confmap/provider/yamlprovider"
	"go.opentelemetry.io/collector/otelcol"

	"go.chromium.org/build/siso/auth/cred"

	_ "embed"
)

//go:embed config.yaml
var collectorConfig []byte

type Command struct {
	authOpts         cred.Options
	version          string
	projectID        string
	collectorAddress string
}

func Cmd(authOpts cred.Options, version string) *Command {
	return &Command{
		authOpts: authOpts,
		version:  version,
	}
}

func (*Command) Name() string {
	return "collector"
}

func (*Command) Synopsis() string {
	return "OTEL collector daemon"
}

func (*Command) Usage() string {
	return "Starts the OTEL collector daemon.\n"
}

func (c *Command) SetFlags(flagSet *flag.FlagSet) {
	flagSet.StringVar(&c.projectID, "project", os.Getenv("SISO_PROJECT"), "cloud project ID. can be set by $SISO_PROJECT")
	flagSet.StringVar(&c.collectorAddress, "collector_address", "127.0.0.1:4317", `address to listen on for collector. Can be path for unix socket unix:///path/to/socket or host:port.`)
}

func (c *Command) Execute(ctx context.Context, f *flag.FlagSet, args ...any) subcommands.ExitStatus {
	credential, err := cred.New(ctx, "https://logging.googleapis.com/", c.authOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return subcommands.ExitFailure
	}

	set := otelcol.CollectorSettings{
		Factories: func() (otelcol.Factories, error) {
			return components(credential, c.projectID, c.collectorAddress)
		},
		ConfigProviderSettings: otelcol.ConfigProviderSettings{
			ResolverSettings: confmap.ResolverSettings{
				ProviderFactories: []confmap.ProviderFactory{
					envprovider.NewFactory(),
					fileprovider.NewFactory(),
					httpprovider.NewFactory(),
					httpsprovider.NewFactory(),
					yamlprovider.NewFactory(),
				},
			},
		},
		ProviderModules: map[string]string{
			envprovider.NewFactory().Create(confmap.ProviderSettings{}).Scheme():   "go.opentelemetry.io/collector/confmap/provider/envprovider",
			fileprovider.NewFactory().Create(confmap.ProviderSettings{}).Scheme():  "go.opentelemetry.io/collector/confmap/provider/fileprovider",
			httpprovider.NewFactory().Create(confmap.ProviderSettings{}).Scheme():  "go.opentelemetry.io/collector/confmap/provider/httpprovider",
			httpsprovider.NewFactory().Create(confmap.ProviderSettings{}).Scheme(): "go.opentelemetry.io/collector/confmap/provider/httpsprovider",
			yamlprovider.NewFactory().Create(confmap.ProviderSettings{}).Scheme():  "go.opentelemetry.io/collector/confmap/provider/yamlprovider",
		},
		ConverterModules: []string{},
	}

	otelcolArgs := f.Args()
	otelcolArgs = append(otelcolArgs, "--config", "yaml:"+string(collectorConfig))

	if err := run(set, otelcolArgs); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start collector: %v\n", err)
		return subcommands.ExitFailure
	}
	return subcommands.ExitSuccess
}

func runInteractive(params otelcol.CollectorSettings, args []string) error {
	cmd := otelcol.NewCommand(params)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		return fmt.Errorf("collector server run finished with error: %v", err)
	}

	return nil
}
