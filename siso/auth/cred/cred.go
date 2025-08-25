// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package cred provides gRPC / API credentials to authenticate to network services.
package cred

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/oauth"

	"go.chromium.org/build/siso/o11y/clog"
)

// Cred holds credentials and derived values.
type Cred struct {
	// Type is credential type. e.g. "luci-auth", "gcloud", etc.
	Type string

	// Email is authenticated email.
	Email string

	perRPCCredentials credentials.PerRPCCredentials
	tokenSource       oauth2.TokenSource
}

// Options is an options for credentials.
type Options struct {
	Type              string
	PerRPCCredentials credentials.PerRPCCredentials
	// TokenSource is used when PerRPCCredentials is not set.
	TokenSource oauth2.TokenSource
}

// AuthOpts returns the LUCI auth options that Siso uses.
func AuthOpts(credHelperPath string, args ...string) Options {
	var perRPCCredentials credentials.PerRPCCredentials
	var tokenSource oauth2.TokenSource
	base := filepath.Base(credHelperPath)
	authType := strings.TrimSuffix(base, filepath.Ext(base))
	switch authType {
	case "luci-auth":
		tokenSource = &luciAuthTokenSource{luciAuthPath: credHelperPath, contextArgs: args}
	case "gcloud", "":
		tokenSource = gcloudTokenSource{}
	default:
		h := &credHelper{path: credHelperPath}
		perRPCCredentials = h
		tokenSource = &credHelperGoogle{h: h}
	}
	return Options{
		Type:              authType,
		PerRPCCredentials: perRPCCredentials,
		TokenSource:       tokenSource,
	}
}

// New creates a Cred using LUCI auth's default options.
// It ensures that the user is logged in and returns an error otherwise.
func New(ctx context.Context, uri string, opts Options) (Cred, error) {
	var t string
	if opts.TokenSource == nil {
		return Cred{}, nil
	}
	if opts.PerRPCCredentials != nil && uri != "" {
		_, err := opts.PerRPCCredentials.GetRequestMetadata(ctx, uri)
		if err == nil {
			t := "credential_helper"
			if ch, ok := opts.PerRPCCredentials.(*credHelper); ok {
				t = ch.path
			}
			return Cred{
				Type:              t,
				perRPCCredentials: opts.PerRPCCredentials,
				tokenSource:       opts.TokenSource,
			}, nil
		}
		clog.Warningf(ctx, "failed to get perRPCCredentials for %q: %v", uri, err)
	}
	var email string
	ts := opts.TokenSource
	tok, err := ts.Token()
	if err != nil {
		if ctx.Err() != nil {
			return Cred{}, err
		}
		if errors.Is(err, errNoAuthorization) {
			if ch, ok := ts.(*credHelperGoogle); ok {
				t = ch.h.path
				clog.Warningf(ctx, "use auth %s, no token source %v", ch.h.path, err)
			} else {
				t = fmt.Sprintf("%T", ts)
				clog.Warningf(ctx, "use auth %T, no token source: %v", ts, err)
			}
			ts = nil
		} else {
			switch opts.Type {
			case "luci-auth", "gcloud", "":
				return Cred{}, fmt.Errorf("need to run `siso login`: %w", err)
			default:
				return Cred{}, err
			}
		}
	} else {
		t, _ = tok.Extra("x-token-source").(string)
		email, _ = tok.Extra("x-token-email").(string)
		clog.Infof(ctx, "use auth %v email: %s", t, email)
		ts = oauth2.ReuseTokenSource(tok, ts)
	}
	perRPCCredentials := opts.PerRPCCredentials
	if perRPCCredentials == nil {
		perRPCCredentials = oauth.TokenSource{
			TokenSource: ts,
		}
	}
	return Cred{
		Type:              t,
		Email:             email,
		perRPCCredentials: perRPCCredentials,
		tokenSource:       ts,
	}, nil
}

// grpcDialOptions returns grpc's dial options to use the credential.
func (c Cred) grpcDialOptions() []grpc.DialOption {
	perRPCCredentials := c.perRPCCredentials
	if perRPCCredentials == nil {
		return nil
	}
	return []grpc.DialOption{
		grpc.WithPerRPCCredentials(perRPCCredentials),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})),
	}
}

// ClientOptions returns client options to use the credential.
func (c Cred) ClientOptions() []option.ClientOption {
	dopts := c.grpcDialOptions()
	if len(dopts) > 0 {
		copts := []option.ClientOption{
			// disable Google Application Default, and use PerRPCCredentials in dial option.
			// https://github.com/googleapis/google-api-go-client/issues/3149
			option.WithoutAuthentication(),
		}
		for _, opt := range dopts {
			copts = append(copts, option.WithGRPCDialOption(opt))
		}
		return copts
	}
	if c.tokenSource == nil {
		return nil
	}
	return []option.ClientOption{
		option.WithTokenSource(c.tokenSource),
	}
}
