// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninja

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	"cloud.google.com/go/logging"
	"cloud.google.com/go/profiler"
	cloudmetric "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric"
	log "github.com/golang/glog"
	"github.com/klauspost/cpuid/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	smetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	mrpb "google.golang.org/genproto/googleapis/api/monitoredres"
	rspb "google.golang.org/genproto/googleapis/devtools/resultstore/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.chromium.org/build/siso/auth/cred"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/o11y/monitoring"
	"go.chromium.org/build/siso/o11y/resultstore"
	"go.chromium.org/build/siso/o11y/trace"
	"go.chromium.org/build/siso/reapi/merkletree"
	"go.chromium.org/build/siso/ui"
	"go.chromium.org/build/siso/version"
)

func (c *Command) initCloudLogging(ctx context.Context, projectID, execRoot string, credential cred.Cred) (context.Context, string, func(), error) {
	log.Infof("enable cloud logging project=%s id=%s", projectID, c.buildID)

	// log_id: "siso.log" and "siso.step"
	// use generic_task resource
	// https://cloud.google.com/logging/docs/api/v2/resource-list
	// https://cloud.google.com/monitoring/api/resources#tag_generic_task
	// Use a switchable logger to avoid data race.
	// The race happens between `logging.NewClient()` which may start using
	// the logger in background goroutines, and `grpclog.SetLoggerV2()`
	// which sets the logger.
	// `grpclog.SetLoggerV2` is not thread-safe and should only be called once at init time.
	slogger := clog.NewSwitchableGRPCLogger()
	grpclog.SetLoggerV2(slogger)
	client, err := logging.NewClient(ctx, projectID, credential.ClientOptions()...)
	if err != nil {
		return ctx, "", func() {}, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return ctx, "", func() {}, err
	}
	logger, err := clog.New(ctx, client, "siso.log", "siso.step", &mrpb.MonitoredResource{
		Type: "generic_task",
		// should set labels for generic_task.
		// see https://cloud.google.com/logging/docs/api/v2/resource-list
		Labels: map[string]string{
			"project_id": projectID,
			"job":        c.jobID,
			"task_id":    c.buildID,
			"location":   hostname,
			"namespace":  execRoot,
		},
	}, c.enableCollector, c.collectorAddress)
	if err != nil {
		return ctx, "", func() {}, err
	}
	ctx = clog.NewContext(ctx, logger)
	slogger.SetLogger(logger)
	return ctx, logger.URL(), func() {
		errch := make(chan error, 1)
		closeStart := time.Now()
		go func() {
			errch <- logger.Close()
		}()
		timeout := 1 * time.Second
		if !c.fastExit {
			timeout = 10 * time.Second
		}
		// Don't use clog as it's closing Cloud logging client.
		select {
		case <-time.After(timeout):
			log.Warningf("close not finished in %s", timeout)
		case err := <-errch:
			if err != nil {
				log.Warningf("falied to close Cloud logger: %v", err)
			} else {
				log.Infof("cloud logging shutdown took: %s", time.Since(closeStart))
			}
		}
	}, nil
}

func (c *Command) initCloudProfiler(ctx context.Context, projectID string, credential cred.Cred) {
	clog.Infof(ctx, "enable cloud profiler %q in %s", c.cloudProfilerServiceName, projectID)
	config := profiler.Config{
		Service:        c.cloudProfilerServiceName,
		ServiceVersion: fmt.Sprintf("%s/%s", c.version, runtime.GOOS),
		MutexProfiling: true,
		ProjectID:      projectID,
	}
	if metadata.OnGCE() {
		// need to set zone,instance if it seems to run on GCE
		// but metadata failed to reply them. b/376372151
		var err error
		config.Zone, err = metadata.ZoneWithContext(ctx)
		if err != nil {
			clog.Warningf(ctx, "failed to get zone from metadata: %v", err)
			config.Zone = "us-central1-a"
		}
		config.Instance, err = metadata.InstanceNameWithContext(ctx)
		if err != nil {
			clog.Warningf(ctx, "failed to get instnace from metadata: %v", err)
			config.Instance = "non-gce-instance"
		}
	}
	err := profiler.Start(config, credential.ClientOptions()...)
	if err != nil {
		clog.Errorf(ctx, "failed to start cloud profiler: %v", err)
	}
}

func (c *Command) initCloudTrace(ctx context.Context, projectID string, credential cred.Cred) *trace.Exporter {
	clog.Infof(ctx, "enable trace in %s [trace > %s]", projectID, c.traceThreshold)
	traceExporter, err := trace.NewExporter(ctx, trace.Options{
		ProjectID:     projectID,
		ServiceName:   fmt.Sprintf("siso/%s/%s", c.version, runtime.GOOS),
		StepThreshold: c.traceThreshold,
		SpanThreshold: c.traceSpanThreshold,
		ClientOptions: slices.Clone(credential.ClientOptions()),
	})
	if err != nil {
		clog.Errorf(ctx, "failed to start trace exporter: %v", err)
	}
	return traceExporter
}

func (c *Command) initCloudMonitoring(ctx context.Context, credential cred.Cred, metricsProject, rbeProjectID string, labels map[string]string) (*smetric.MeterProvider, error) {
	clog.Infof(ctx, "enable cloud monitoring in %s", metricsProject)
	views, err := monitoring.SetupViews(ctx, c.version, rbeProjectID, labels)
	if err != nil {
		return nil, err
	}
	var exporter smetric.Exporter
	if c.enableCollector {
		exporter = newOTELMetricsExporter(ctx, c.collectorAddress)
	}
	if exporter == nil {
		exporter, err = cloudmetric.New(
			cloudmetric.WithProjectID(metricsProject),
			cloudmetric.WithMonitoringClientOptions(credential.ClientOptions()...),
			cloudmetric.WithMetricDescriptorTypeFormatter(func(metrics metricdata.Metrics) string {
				return fmt.Sprintf("workload.googleapis.com/siso/%s", metrics.Name)
			}),
		)
		if err != nil {
			return nil, err
		}
	}
	mp, err := monitoring.NewMetricProvider(ctx, metricsProject, rbeProjectID, exporter, views)
	if err != nil {
		return nil, err
	}
	otel.SetMeterProvider(mp)
	clog.Infof(ctx, "OpenTelemetry exporter has started in %q for RBE project %q", metricsProject, rbeProjectID)
	return mp, nil
}

func newOTELMetricsExporter(ctx context.Context, collectorAddr string) *otlpmetricgrpc.Exporter {
	conn, err := grpc.NewClient(collectorAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		clog.Warningf(ctx, "failed to create connection to OTLP collector: %v", err)
		return nil
	}

	exporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithGRPCConn(conn))
	if err != nil {
		clog.Warningf(ctx, "failed to create OTLP metric exporter: %v", err)
		return nil
	}
	clog.Infof(ctx, "OTEL metrics exporter to %s", collectorAddr)
	return exporter
}

func (c *Command) buildProperties(ctx context.Context) resultstore.Properties {
	properties := resultstore.Properties{}
	properties.Add("dir", c.dir)
	info := cpuinfo()
	properties.Add("cpu", info)
	info = gcinfo()
	properties.Add("memgc", info)

	ver, err := version.Current()
	if err != nil {
		clog.Warningf(ctx, "version err: %v", err)
	} else if ver.IsProdCIPD() {
		properties.Add("cipd_package_name", ver.CIPD.PackageName)
		properties.Add("cipd_instance_id", ver.CIPD.InstanceID)
	} else if ver.Build != nil {
		properties.Add("go_version", ver.Build.GoVersion)
		properties.Add("go_module_path", ver.Build.Main.Path)
		properties.Add("go_module_version", ver.Build.Main.Version)
		properties.Add("go_module_sum", ver.Build.Main.Sum)
		bs := ver.BuildSettings()
		for _, k := range slices.Sorted(maps.Keys(bs)) {
			v := bs[k]
			properties.Add(k, v)
		}
	}
	properties.Add("job_id", c.jobID)

	return properties
}

func (c *Command) resultStoreCallbackFunc(ctx context.Context, hashFS *hashfs.HashFS, execRoot string, resultstoreUploader *resultstore.Uploader) func() {
	if resultstoreUploader == nil {
		return func() {}
	}
	return func() {
		var ents []merkletree.Entry

		var files []string
		if c.metricsJSON != "" {
			files = append(files, c.metricsJSON)
		}
		// TODO(b/329564182): add other files? e.g. siso_output, siso_trace.json etc.
		if len(files) != 0 {
			var err error
			ents, err = hashFS.Entries(ctx, filepath.Join(execRoot, c.dir), files)
			if err != nil {
				clog.Warningf(ctx, "failed to get entries for %q: %v", files, err)
			}
		}
		ents = append(ents, merkletree.Entry{
			Name: "build.log",
			Data: resultstoreUploader.BuildLogData(),
		})
		spin := ui.Default.NewSpinner()
		spin.Start("uploading to resultstore")
		uerr := resultstoreUploader.UploadFiles(ctx, ents)
		if uerr != nil {
			clog.Warningf(ctx, "failed to upload results: %v", uerr)
		}
		spin.Stop(uerr)
	}
}

func (c *Command) finishResultstore(ctx context.Context, resultstoreUploader *resultstore.Uploader, err *error) func() {
	return func() {
		spin := ui.Default.NewSpinner()
		spin.Start("finishing upload to resultstore")
		exitCode := 0
		if *err != nil {
			exitCode = 1
		}
		cerr := resultstoreUploader.Close(ctx, exitCode)
		if cerr != nil {
			clog.Warningf(ctx, "failed to close resultstore: %v", cerr)
		}
		spin.Stop(cerr)
	}
}

func cpuinfo() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "cpu family=%d model=%d stepping=%d ", cpuid.CPU.Family, cpuid.CPU.Model, cpuid.CPU.Stepping)
	fmt.Fprintf(&sb, "brand=%q vendor=%q ", cpuid.CPU.BrandName, cpuid.CPU.VendorString)
	fmt.Fprintf(&sb, "physicalCores=%d threadsPerCore=%d logicalCores=%d ", cpuid.CPU.PhysicalCores, cpuid.CPU.ThreadsPerCore, cpuid.CPU.LogicalCores)
	fmt.Fprintf(&sb, "vm=%t features=%s", cpuid.CPU.VM(), cpuid.CPU.FeatureSet())
	return sb.String()
}

func gcinfo() string {
	var sb strings.Builder
	memoryLimit := debug.SetMemoryLimit(-1) // not adjust the limit, but retrieve current limit
	if memoryLimit == math.MaxInt64 {
		// initial settings
		fmt.Fprintf(&sb, "memory_limit=unlimited ")
	} else {
		fmt.Fprintf(&sb, "memory_limit=%d (GOMEMLIMIT=%s) ", memoryLimit, os.Getenv("GOMEMLIMIT"))
	}

	gcPercent := debug.SetGCPercent(100) // 100 is default
	if gcPercent < 0 {
		ui.Default.PrintLines(ui.SGR(ui.BackgroundRed, fmt.Sprintf("Garbage collection is disabled. GOGC=%s\n", os.Getenv("GOGC"))))
		fmt.Fprintf(&sb, "gc=off")
	} else {
		fmt.Fprintf(&sb, "gc=%d", gcPercent)
	}
	debug.SetGCPercent(gcPercent) // restore original setting
	if v := os.Getenv("GOGC"); v != "" {
		fmt.Fprintf(&sb, " (GOGC=%s)", v)
	}
	return sb.String()
}

func (c *Command) invocation(ctx context.Context, buildID, projectID, execRoot string, properties resultstore.Properties) *rspb.Invocation {
	var username string
	currentUser, err := user.Current()
	if err != nil {
		clog.Warningf(ctx, "failed to get current user: %v", err)
		username = "unknownuser"
	} else {
		username = currentUser.Username
	}
	hostname, err := os.Hostname()
	if err != nil {
		clog.Warningf(ctx, "failed to get hostname: %v", err)
		hostname = "unknownhost"
	}

	return &rspb.Invocation{
		Timing: &rspb.Timing{
			StartTime: timestamppb.New(c.started),
		},
		InvocationAttributes: &rspb.InvocationAttributes{
			ProjectId:   projectID,
			Users:       []string{username},
			Labels:      []string{"siso", "build"},
			Description: fmt.Sprintf("Invocation ID %s", buildID),
		},
		WorkspaceInfo: &rspb.WorkspaceInfo{
			Hostname:         hostname,
			WorkingDirectory: execRoot,
			ToolTag:          "siso",
			CommandLines:     c.commandLines(),
		},
		Properties: properties,
	}
}

func (c *Command) commandLines() []*rspb.CommandLine {
	var cmdlines []*rspb.CommandLine
	cmdlines = append(cmdlines, &rspb.CommandLine{
		Label:   "original",
		Tool:    os.Args[0],
		Args:    os.Args[1:],
		Command: "ninja",
	})
	cmdline := &rspb.CommandLine{
		Label:   "canonical",
		Tool:    os.Args[0],
		Args:    []string{"ninja"},
		Command: "ninja",
	}
	c.Flags.VisitAll(func(f *flag.Flag) {
		cmdline.Args = append(cmdline.Args, fmt.Sprintf("-%s=%s", f.Name, f.Value.String()))
	})
	cmdline.Args = append(cmdline.Args, c.Flags.Args()...)
	cmdlines = append(cmdlines, cmdline)
	return cmdlines
}
