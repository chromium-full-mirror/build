// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Kajiya is an RBE-compatible REAPI backend implementation used as a testing
// server during development of Chromium's new build tooling. It is not meant
// for production use, but can be very useful for local testing of any remote
// execution related code.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"

	"go.chromium.org/build/kajiya/actioncache"
	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/capabilities"
	"go.chromium.org/build/kajiya/execution"
	"go.chromium.org/build/kajiya/execution/localexec"
	"go.chromium.org/build/kajiya/log"
	"go.chromium.org/build/kajiya/server"

	_ "net/http/pprof" // import to let pprof register its HTTP handlers
)

var (
	dataDir                = flag.String("dir", getDefaultDataDir(), "the directory to store our data in")
	listen                 = flag.String("listen", "localhost:50051", "the address to listen on (e.g. localhost:50051 or unix:///tmp/kajiya.sock)")
	enableCache            = flag.Bool("cache", true, "whether to enable the action cache service")
	enableExecution        = flag.Bool("execution", true, "whether to enable the execution service")
	pprofAddr              = flag.String("pprof_addr", "", `listen address for "go tool pprof". e.g. "localhost:6060"`)
	cpuprofile             = flag.String("cpuprofile", "", "write cpu profile to file")
	memprofile             = flag.String("memprofile", "", "write memory profile to this file")
	memprofileInterval     = flag.Duration("memprofile_interval", 0, `if non-zero, dump heap profiles at this interval to rolling files derived from -memprofile (e.g. "kajiya-memory.0001.pprof"); the bare -memprofile path is not written.`)
	memprofileGC           = flag.Bool("memprofile_gc", true, "run runtime.GC() before each heap profile dump for a cleaner inuse view")
	mutexprofile           = flag.String("mutexprofile", "", "write mutex profile to this file")
	blockprofile           = flag.String("blockprofile", "", "write block profile to this file")
	blockprofRate          = flag.Int("blockprof_rate", 0, "block profile rate")
	mutexprofFrac          = flag.Int("mutexprof_frac", 0, "mutex profile fraction")
	traceFile              = flag.String("trace", "", `go trace output for "go tool trace"`)
	tlsCertFile            = flag.String("tls_cert_file", "", "TLS certificate file")
	tlsKeyFile             = flag.String("tls_key_file", "", "TLS key file")
	sandboxStrategy        = flag.String("sandbox", "overlayfs", "sandbox strategy to use (one of: files, overlayfs, nested-overlayfs)")
	quiet                  = flag.Bool("quiet", false, "if true, print only warnings and errors in log output")
	maxRecvMsgSize         = flag.Int("max_recv_msg_size", 0, "maximum size of a single gRPC message that can be received")
	maxBatchTotalSizeBytes = flag.Int64("max_batch_total_size_bytes", 0, "maximum combined total size of blobs in batch requests (0 means unlimited)")
	skipCASValidation      = flag.Bool("skip_cas_validation", false, "skip CAS integrity validation on startup (faster startup, but won't detect corrupted blobs)")
	allowHostFS            = flag.Bool("allow_host_fs", false, "allow actions without a container image to run with access to the host filesystem")

	sb localexec.SandboxStrategy
)

func getDefaultDataDir() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "kajiya")
}

func main() {
	os.Exit(run(context.Background()))
}

func run(ctx context.Context) int {
	flag.Parse()

	level := slog.LevelInfo
	if *quiet {
		level = slog.LevelWarn
	}
	slog.SetDefault(slog.New(log.NewPrettyHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})))

	// Validate the sandbox strategy flag.
	if *sandboxStrategy == "files" {
		sb = localexec.Files
	} else if *sandboxStrategy == "overlayfs" && runtime.GOOS == "linux" {
		sb = localexec.OverlayFS
	} else if *sandboxStrategy == "nested-overlayfs" && runtime.GOOS == "linux" {
		sb = localexec.NestedOverlayFS
	} else {
		slog.Error("invalid sandbox strategy", "name", *sandboxStrategy)
		flag.Usage()
		return 2
	}

	// Reset the umask to a known value, so we know which permissions newly
	// created files and directories will have.
	blobstore.ResetUmask()

	if *blockprofile != "" && *blockprofRate == 0 {
		*blockprofRate = 1
	}
	if *mutexprofile != "" && *mutexprofFrac == 0 {
		*mutexprofFrac = 1
	}
	if *blockprofRate > 0 {
		runtime.SetBlockProfileRate(*blockprofRate)
	}
	if *mutexprofFrac > 0 {
		runtime.SetMutexProfileFraction(*mutexprofFrac)
	}

	// Enable CPU profiling if requested.
	if *cpuprofile != "" {
		slog.Info("CPU profile logging to file", "path", *cpuprofile)
		f, err := os.Create(*cpuprofile)
		if err != nil {
			slog.Error("failed to create file for CPU profile", "error", err)
			return 1
		}
		err = pprof.StartCPUProfile(f)
		if err != nil {
			_ = f.Close()
			slog.Error("failed to start CPU profiler", "error", err)
			return 1
		}
		defer func() {
			pprof.StopCPUProfile()
			if err := f.Close(); err != nil {
				slog.Error("failed to close CPU profile file", "error", err)
			}
		}()
	}

	// Save a heap profile to disk on exit, or periodically to rolling files.
	if *memprofile != "" {
		if *memprofileInterval > 0 {
			stop := startPeriodicMemprofile(*memprofile, *memprofileInterval, *memprofileGC)
			defer stop()
		} else {
			f, err := os.Create(*memprofile)
			if err != nil {
				slog.Error("failed to create memprofile file", "error", err)
				return 1
			}
			defer func() {
				if *memprofileGC {
					runtime.GC()
				}
				if err := pprof.WriteHeapProfile(f); err != nil {
					slog.Error("failed to write heap profile", "error", err)
				}
				if err := f.Close(); err != nil {
					slog.Error("failed to close memprofile file", "error", err)
				}
			}()
		}
	}

	// Save a mutex profile to disk on exit.
	if *mutexprofile != "" {
		f, err := os.Create(*mutexprofile)
		if err != nil {
			slog.Error("failed to create mutexprofile file", "error", err)
			return 1
		}
		defer func() {
			if err := pprof.Lookup("mutex").WriteTo(f, 0); err != nil {
				slog.Error("failed to write mutex profile", "error", err)
			}
			if err := f.Close(); err != nil {
				slog.Error("failed to close mutexprofile file", "error", err)
			}
		}()
	}

	// Save a block profile to disk on exit.
	if *blockprofile != "" {
		f, err := os.Create(*blockprofile)
		if err != nil {
			slog.Error("failed to create blockprofile file", "error", err)
			return 1
		}
		defer func() {
			if err := pprof.Lookup("block").WriteTo(f, 0); err != nil {
				slog.Error("failed to write block profile", "error", err)
			}
			if err := f.Close(); err != nil {
				slog.Error("failed to close blockprofile file", "error", err)
			}
		}()
	}

	// Save a go trace to disk during execution.
	if *traceFile != "" {
		slog.Info("enabling go trace", "path", *traceFile)
		f, err := os.Create(*traceFile)
		if err != nil {
			slog.Error("failed to create go trace output file", "error", err)
			return 1
		}
		defer func() {
			slog.Info("go trace written", "command", fmt.Sprintf("go tool trace %s", *traceFile))
			if err := f.Close(); err != nil {
				slog.Error("failed to close go trace output file", "error", err)
			}
		}()
		if err := trace.Start(f); err != nil {
			slog.Error("failed to start go trace", "error", err)
			return 1
		}
		defer trace.Stop()
	}

	// Start an HTTP server that can be used to profile Kajiya during runtime if requested.
	if *pprofAddr != "" {
		// https://pkg.go.dev/net/http/pprof
		slog.Info("pprof is enabled", "url", fmt.Sprintf("http://%s/debug/pprof/", *pprofAddr))
		go func() {
			if err := http.ListenAndServe(*pprofAddr, nil); !errors.Is(err, http.ErrServerClosed) {
				slog.Error("pprof server failed", "error", err)
			}
		}()
		defer func() {
			slog.Info("pprof is still listening", "url", fmt.Sprintf("http://%s/debug/pprof/", *pprofAddr))
			slog.Info("press Ctrl-C to terminate the process")
			sigch := make(chan os.Signal, 1)
			signal.Notify(sigch, os.Interrupt, syscall.SIGTERM)
			<-sigch
		}()
	}

	// Ensure our data directory exists.
	if *dataDir == "" {
		slog.Error("no data directory specified")
		flag.Usage()
		return 2
	}
	slog.Info("using data directory", "dir", *dataDir)
	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		slog.Error("failed to create data directory", "error", err)
		return 1
	}

	// Listen on the specified address.
	network, addr := parseAddress(*listen)
	listener, err := net.Listen(network, addr)
	if err != nil {
		slog.Error("failed to listen", "error", err)
		return 1
	}
	slog.Info("gRPC listening", "address", listener.Addr())

	// Create the gRPC server and register the services.
	grpcServer, err := createServer(ctx, *dataDir)
	if err != nil {
		slog.Error("failed to create server", "error", err)
		return 1
	}

	// Handle interrupts gracefully.
	HandleInterrupt(func() {
		grpcServer.GracefulStop()
	})

	// Start serving.
	if err := grpcServer.Serve(listener); err != nil {
		slog.Error("gRPC failed to serve", "error", err)
		return 1
	}

	return 0
}

// parseAddress parses the listen address from the command line flag.
// The address can be a TCP address (e.g. localhost:50051) or a Unix domain socket (e.g. unix:///tmp/kajiya.sock).
func parseAddress(addr string) (string, string) {
	network := "tcp"
	if strings.HasPrefix(addr, "unix://") {
		network = "unix"
		addr = addr[len("unix://"):]
	}
	return network, addr
}

// createServer creates a new gRPC server and registers the services.
func createServer(ctx context.Context, dataDir string) (*grpc.Server, error) {
	// If either the cert or key file is specified, both must be.
	if (*tlsCertFile == "") != (*tlsKeyFile == "") {
		return nil, fmt.Errorf("both --tls_cert_file and --tls_key_file must be specified")
	}

	cfg := server.Config{
		MaxBatchTotalSizeBytes: *maxBatchTotalSizeBytes,
		MaxRecvMsgSize:         *maxRecvMsgSize,
	}
	if cfg.MaxRecvMsgSize == 0 {
		cfg.MaxRecvMsgSize = cfg.RecommendedMaxRecvMsgSize()
		slog.Info("using gRPC max receive message size", "size", cfg.MaxRecvMsgSize)
	} else if cfg.MaxRecvMsgSize < cfg.RecommendedMaxRecvMsgSize() {
		slog.Warn("gRPC max receive message size is too small, consider increasing your -max_recv_msg_size",
			"got", cfg.MaxRecvMsgSize, "want", cfg.RecommendedMaxRecvMsgSize())
	}

	// Create tls based credential.
	var opts []grpc.ServerOption
	if *tlsCertFile != "" {
		slog.Info("using TLS", "cert", filepath.Base(*tlsCertFile), "key", filepath.Base(*tlsKeyFile))
		creds, err := credentials.NewServerTLSFromFile(*tlsCertFile, *tlsKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS certificate and key: %v", err)
		}
		opts = append(opts, grpc.Creds(creds))
	}
	opts = append(opts, grpc.MaxRecvMsgSize(cfg.MaxRecvMsgSize))

	s := grpc.NewServer(opts...)

	capabilities.Register(s, cfg)
	slog.Info("capabilities service registered")

	// Create a CAS backed by a local filesystem.
	casDir := filepath.Join(dataDir, "cas")
	cas, err := blobstore.New(ctx, casDir, *skipCASValidation)
	if err != nil {
		return nil, err
	}

	// CAS service.
	blobstore.Register(s, cas, cfg)
	slog.Info("content-addressable storage service registered")

	// Action cache service.
	var ac *actioncache.ActionCache
	if *enableCache {
		acDir := filepath.Join(dataDir, "ac")
		ac, err = actioncache.New(ctx, acDir, cas)
		if err != nil {
			return nil, err
		}

		err = actioncache.Register(s, ac, cas)
		if err != nil {
			return nil, err
		}
		slog.Info("action cache service registered")
	} else {
		slog.Warn("action cache service disabled")
	}

	// Execution service.
	if *enableExecution {
		execDir := filepath.Join(dataDir, "exec")
		executor, err := localexec.New(execDir, cas, sb, *allowHostFS)
		if err != nil {
			return nil, err
		}

		err = execution.Register(s, executor, ac, cas)
		if err != nil {
			return nil, err
		}
		slog.Info("execution service registered")
	} else {
		slog.Warn("execution service disabled")
	}

	// Register the reflection service provided by gRPC.
	reflection.Register(s)
	slog.Info("gRPC reflection service registered")

	return s, nil
}

// startPeriodicMemprofile launches a goroutine that writes a heap profile to a
// rolling file every interval. The base path's extension (if any) is preserved
// after a 4-digit sequence number, so "/tmp/m.pprof" becomes
// "/tmp/m.0001.pprof", "/tmp/m.0002.pprof", and so on. The returned function
// stops the goroutine and writes one final dump.
func startPeriodicMemprofile(base string, interval time.Duration, gc bool) func() {
	dir, file := filepath.Split(base)
	ext := filepath.Ext(file)
	stem := strings.TrimSuffix(file, ext)
	var seq atomic.Uint64
	dump := func() {
		n := seq.Add(1)
		path := filepath.Join(dir, fmt.Sprintf("%s.%04d%s", stem, n, ext))
		f, err := os.Create(path)
		if err != nil {
			slog.Error("failed to create memprofile file", "path", path, "error", err)
			return
		}
		defer func() {
			if err := f.Close(); err != nil {
				slog.Error("failed to close memprofile file", "path", path, "error", err)
			}
		}()
		if gc {
			runtime.GC()
		}
		if err := pprof.WriteHeapProfile(f); err != nil {
			slog.Error("failed to write heap profile", "path", path, "error", err)
		}
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-t.C:
				dump()
			}
		}
	}()
	return func() {
		close(stopCh)
		<-doneCh
		dump()
	}
}

// HandleInterrupt calls 'fn' in a separate goroutine on SIGTERM or Ctrl+C.
//
// When SIGTERM or Ctrl+C comes for a second time, logs to stderr and kills
// the process immediately via os.Exit(1).
//
// Returns a callback that can be used to remove the installed signal handlers.
func HandleInterrupt(fn func()) (stopper func()) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		handled := false
		for range ch {
			if handled {
				slog.Error("received second interrupt signal, exiting now")
				os.Exit(1)
			}
			slog.Warn("received signal, attempting graceful shutdown")
			handled = true
			go fn()
		}
	}()
	return func() {
		signal.Stop(ch)
		close(ch)
	}
}
