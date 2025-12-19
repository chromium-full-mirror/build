// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninja

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.chromium.org/build/siso/auth/cred"
	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/cachestore"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/reapi"
	"go.chromium.org/build/siso/reapi/digest"
)

type localCacheOptions struct {
	localCacheEnable bool
	cacheDir         string
}

func (c *Command) setLocalCacheFlags(flagSet *flag.FlagSet) {
	flagSet.BoolVar(&c.localCacheEnable, "local_cache_enable", false, "local cache enable")
	flagSet.StringVar(&c.cacheDir, "cache_dir", defaultCacheDir(), "cache directory")
}

func initDataSource(ctx context.Context, credential cred.Cred, localCacheOpts localCacheOptions, reopt *reapi.Option) (dataSource, error) {
	layeredCache := build.NewLayeredCache()
	if localCacheOpts.localCacheEnable {
		cache, err := build.NewLocalCache(localCacheOpts.cacheDir)
		if err != nil {
			clog.Warningf(ctx, "failed to create local cache - no local cache enabled: %v", err)
		} else {
			layeredCache.AddLayer(cache)
			cache.GarbageCollectIfRequired(ctx)
		}
	}
	var ds dataSource
	err := reopt.CheckValid()
	if err == nil {
		ds.client, err = reapi.New(ctx, credential, *reopt)
		if err != nil {
			return ds, err
		}
		layeredCache.AddLayer(ds.client.CacheStore())
	}
	ds.cache = layeredCache
	return ds, nil
}

type dataSource struct {
	cache  cachestore.CacheStore
	client *reapi.Client
}

func (ds dataSource) Close(ctx context.Context) error {
	if ds.client == nil {
		return nil
	}
	return ds.client.Close()
}

func (ds dataSource) DigestData(ctx context.Context, d digest.Digest, fname string) digest.Data {
	return digest.NewData(ds.Source(ctx, d, fname), d)
}

func (ds dataSource) Source(_ context.Context, d digest.Digest, fname string) digest.Source {
	return source{
		dataSource: ds,
		d:          d,
		fname:      fname,
	}
}

type source struct {
	dataSource dataSource
	d          digest.Digest
	fname      string
}

func (s source) Open(ctx context.Context) (io.ReadCloser, error) {
	var r io.ReadCloser
	var err error
	if s.dataSource.cache != nil {
		src := s.dataSource.cache.Source(ctx, s.d, s.fname)
		if src != nil {
			r, err = src.Open(ctx)
			if err == nil {
				return r, nil
			}
		}
		// fallback
	}
	if s.dataSource.client != nil {
		var buf []byte
		buf, err = s.dataSource.client.Get(ctx, s.d, s.fname)
		if err == nil {
			return io.NopCloser(bytes.NewReader(buf)), nil
		}
		// fallback
	}
	// ctx may be deadline exceeded or canceled.
	// if so, return such error.
	// DeadlineExceeded would trigger retry in hashfs flush.
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	// siso process runs at some directory, but
	// s.fname may not be relative to the working directory.
	// Actually, it is exec-root relative if it is created by
	// *Cmd.entriesFromResult, and failed to open as such path
	// doesn't exist. return with better error message.
	if !filepath.IsAbs(s.fname) {
		return nil, fmt.Errorf("failed to fetch source %v for %q: %w", s.d, s.fname, err)
	}
	// no reapi configured. use local file?
	f, err := os.Open(s.fname)
	return f, err
}

func (s source) String() string {
	return fmt.Sprintf("dataSource:%s", s.fname)
}
