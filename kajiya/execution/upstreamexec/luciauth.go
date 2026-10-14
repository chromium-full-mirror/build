// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package upstreamexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// luciAuthScopes are the OAuth2 scopes requested from luci-auth. Google RBE
// requires cloud-platform. luci-auth issues tokens only for a scope set that
// the user has logged in with, so these are the same scopes that `siso login`
// requests, and one login serves both.
var luciAuthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
}

// luciAuthTokenTimeout bounds one `luci-auth token` invocation, which may
// refresh the token over the network.
const luciAuthTokenTimeout = 30 * time.Second

// luciAuthTokenSource returns OAuth2 access tokens by running
// `luci-auth token --json-output -`. It does not support luci-auth's
// `context` mode.
type luciAuthTokenSource struct {
	// binPath is the path of the luci-auth executable.
	binPath string
}

// newLuciAuthTokenSource looks up `luci-auth` on PATH and returns a token
// source that reuses each token until it expires.
func newLuciAuthTokenSource() (oauth2.TokenSource, error) {
	path, err := exec.LookPath("luci-auth")
	if err != nil {
		return nil, fmt.Errorf("luci-auth not found in PATH: %w", err)
	}
	return oauth2.ReuseTokenSource(nil, &luciAuthTokenSource{binPath: path}), nil
}

// luciAuthTokenResponse is the JSON object that
// `luci-auth token --json-output -` writes, for example:
//
//	{"token":"<token>","expiry":1745678901}
type luciAuthTokenResponse struct {
	// Token is the OAuth2 access token.
	Token string `json:"token"`

	// Expiry is the token's expiry time in seconds since the Unix epoch.
	Expiry int64 `json:"expiry"`
}

// Token implements oauth2.TokenSource. It returns an error, including
// luci-auth's stderr, if luci-auth fails, for example because the user is not
// logged in, or if its output holds no token.
func (ts *luciAuthTokenSource) Token() (*oauth2.Token, error) {
	// Run luci-auth.
	ctx, cancel := context.WithTimeout(context.Background(), luciAuthTokenTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ts.binPath,
		"token",
		"--json-output", "-",
		"--scopes", strings.Join(luciAuthScopes, " "),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("luci-auth token failed: %w\nstderr: %s", err, stderr.String())
	}

	// Parse the token from its output.
	var resp luciAuthTokenResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse luci-auth response %q: %w", stdout.Bytes(), err)
	}
	if resp.Token == "" {
		return nil, errors.New("luci-auth returned an empty token")
	}
	return &oauth2.Token{
		AccessToken: resp.Token,
		Expiry:      time.Unix(resp.Expiry, 0),
		TokenType:   "Bearer",
	}, nil
}
