// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package upstreamexec

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeFakeLuciAuth writes a luci-auth stand-in that records its arguments
// one per line in the returned args file, prints stdout and stderr, and exits
// with exitCode. It returns the paths of the script and the args file.
func writeFakeLuciAuth(t *testing.T, stdout, stderr string, exitCode int) (binPath, argsPath string) {
	t.Helper()
	dir := t.TempDir()

	// Store the outputs in files, which spares quoting them for the shell.
	stdoutPath := filepath.Join(dir, "stdout")
	stderrPath := filepath.Join(dir, "stderr")
	if err := os.WriteFile(stdoutPath, []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stderrPath, []byte(stderr), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write the script.
	argsPath = filepath.Join(dir, "args")
	binPath = filepath.Join(dir, "luci-auth")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s'\ncat '%s'\ncat '%s' >&2\nexit %d\n",
		argsPath, stdoutPath, stderrPath, exitCode)
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binPath, argsPath
}

// TestLuciAuthTokenSource verifies how Token invokes luci-auth and turns its
// output into a token or an error.
func TestLuciAuthTokenSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the luci-auth stand-in is a shell script")
	}

	tests := []struct {
		name     string
		stdout   string
		stderr   string
		exitCode int

		// wantErr is a substring of the error, or empty for success.
		wantErr string
	}{
		{
			name:   "valid_token",
			stdout: `{"token":"secret","expiry":1745678901}`,
		},
		{
			name:     "not_logged_in",
			stderr:   "Not logged in.",
			exitCode: 1,
			wantErr:  "Not logged in.",
		},
		{
			name:    "invalid_json",
			stdout:  "not json",
			wantErr: "failed to parse luci-auth response",
		},
		{
			name:    "empty_token",
			stdout:  `{"token":"","expiry":1745678901}`,
			wantErr: "empty token",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Run Token against the stand-in.
			binPath, argsPath := writeFakeLuciAuth(t, tc.stdout, tc.stderr, tc.exitCode)
			ts := &luciAuthTokenSource{binPath: binPath}
			tok, err := ts.Token()

			// Check the arguments luci-auth received.
			args, readErr := os.ReadFile(argsPath)
			if readErr != nil {
				t.Fatalf("luci-auth did not record its arguments: %v", readErr)
			}
			wantArgs := []string{"token", "--json-output", "-", "--scopes", strings.Join(luciAuthScopes, " ")}
			if got := strings.Split(strings.TrimSuffix(string(args), "\n"), "\n"); !slices.Equal(got, wantArgs) {
				t.Errorf("luci-auth args = %q, want %q", got, wantArgs)
			}

			// Check the result.
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Token() = %v, want error containing %q", tok, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("Token() error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Token() error = %v", err)
			}
			if got, want := tok.AccessToken, "secret"; got != want {
				t.Errorf("AccessToken = %q, want %q", got, want)
			}
			if got, want := tok.Expiry, time.Unix(1745678901, 0); !got.Equal(want) {
				t.Errorf("Expiry = %v, want %v", got, want)
			}
			if got, want := tok.TokenType, "Bearer"; got != want {
				t.Errorf("TokenType = %q, want %q", got, want)
			}
		})
	}
}

// TestDialFailsWithoutLuciAuth verifies that a missing luci-auth binary fails
// Dial rather than the first RPC.
func TestDialFailsWithoutLuciAuth(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Dial("remotebuildexecution.googleapis.com:443", false); err == nil {
		t.Error("Dial succeeded without luci-auth on PATH")
	}
}
